package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"

	"text-annotation-platform/config"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"

	"golang.org/x/crypto/bcrypt"
)

const (
	annotatorCount = 2208
	datasetCount   = 152
)

type demoTagDef struct {
	Name  string
	Color string
}

func main() {
	cleanOnly := flag.Bool("clean", false, "clean previously seeded demo data and exit")
	flag.Parse()

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	dbRepo, err := repository.NewDB(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect database failed: %v", err)
	}

	if err := cleanSeedData(dbRepo); err != nil {
		log.Fatalf("clean seed data failed: %v", err)
	}
	if *cleanOnly {
		log.Println("demo seed data cleaned")
		return
	}

	rng := rand.New(rand.NewSource(20260303))

	annotatorIDs, err := seedAnnotators(dbRepo, annotatorCount)
	if err != nil {
		log.Fatalf("seed annotators failed: %v", err)
	}
	log.Printf("seeded annotators: %d", len(annotatorIDs))

	tagIDs, err := seedDemoTags(dbRepo)
	if err != nil {
		log.Fatalf("seed tags failed: %v", err)
	}

	datasets, err := seedDatasets(dbRepo, datasetCount, tagIDs, rng)
	if err != nil {
		log.Fatalf("seed datasets failed: %v", err)
	}
	log.Printf("seeded datasets: %d", len(datasets))

	log.Printf("seed completed: datasets=%d (empty), annotators=%d", len(datasets), len(annotatorIDs))
	log.Println("tip: start backend with DEMO_MODE=true to use demo dashboard statistics")
}

func cleanSeedData(dbRepo *repository.DB) error {
	var datasetIDs []uint
	if err := dbRepo.DB.Model(&dbmodel.Dataset{}).
		Where("name LIKE ? OR name LIKE ?", "演示数据集-%", "数据集-%").
		Pluck("id", &datasetIDs).Error; err != nil {
		return err
	}
	if len(datasetIDs) > 0 {
		if err := dbRepo.DB.Where("dataset_id IN ?", datasetIDs).Delete(&dbmodel.DatasetTag{}).Error; err != nil {
			return err
		}
	}
	if err := dbRepo.DB.Where("name IN ?", []string{"精标完成", "精标进行中", "待上传", "质检通过", "疑难复核", "重点案件"}).Delete(&dbmodel.Tag{}).Error; err != nil {
		return err
	}
	if err := dbRepo.DB.Where("username LIKE ?", "zh_%").Delete(&dbmodel.User{}).Error; err != nil {
		return err
	}
	if err := dbRepo.DB.Where("name LIKE ? OR name LIKE ?", "演示数据集-%", "数据集-%").Delete(&dbmodel.Dataset{}).Error; err != nil {
		return err
	}
	// 演示文档存于 Postgres documents 表。
	return dbRepo.DB.Exec(`DELETE FROM documents WHERE doc_key LIKE 'demo_doc_%'`).Error
}

func seedAnnotators(dbRepo *repository.DB, count int) ([]uint, error) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("demo123"), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password failed: %w", err)
	}

	ids := make([]uint, 0, count)
	for i := 1; i <= count; i++ {
		username := fmt.Sprintf("zh_%05d", i)
		displayName := username
		user := dbmodel.User{
			Username:     username,
			PasswordHash: string(passwordHash),
			Role:         "annotator",
			Status:       "active",
			DisplayName:  displayName,
		}
		if err := dbRepo.DB.Create(&user).Error; err != nil {
			return nil, err
		}
		ids = append(ids, user.ID)
	}
	return ids, nil
}

func seedDemoTags(dbRepo *repository.DB) (map[string]uint, error) {
	tags := []demoTagDef{
		{Name: "精标完成", Color: "#000000"},
		{Name: "精标进行中", Color: "#000000"},
		{Name: "待上传", Color: "#000000"},
		{Name: "质检通过", Color: "#000000"},
		{Name: "疑难复核", Color: "#000000"},
		{Name: "重点案件", Color: "#000000"},
	}

	ids := make(map[string]uint, len(tags))
	for _, t := range tags {
		if existing, err := dbRepo.FindTagByName(context.Background(), t.Name, "dataset"); err == nil {
			if existing.Color != t.Color {
				if updateErr := dbRepo.DB.Model(&dbmodel.Tag{}).Where("id = ?", existing.ID).Update("color", t.Color).Error; updateErr != nil {
					return nil, updateErr
				}
			}
			ids[t.Name] = existing.ID
			continue
		}

		tag := dbmodel.Tag{Name: t.Name, Color: t.Color}
		if createErr := dbRepo.DB.Create(&tag).Error; createErr != nil {
			return nil, createErr
		}
		ids[t.Name] = tag.ID
	}

	return ids, nil
}

func seedDatasets(dbRepo *repository.DB, count int, tagIDs map[string]uint, rng *rand.Rand) ([]dbmodel.Dataset, error) {
	datasets := make([]dbmodel.Dataset, 0, count)
	for i := 1; i <= count; i++ {
		ds := dbmodel.Dataset{
			Name:           fmt.Sprintf("数据集-%03d", i),
			OwnerID:        1,
			UserID:         1,
			AnnotationType: "qa",
			DocCount:       0,
			CaseType:       pickCaseType(i),
		}
		if err := dbRepo.DB.Create(&ds).Error; err != nil {
			return nil, err
		}

		if err := attachDatasetTags(dbRepo, ds.ID, i, tagIDs, rng); err != nil {
			return nil, err
		}

		datasets = append(datasets, ds)
	}
	return datasets, nil
}

func attachDatasetTags(dbRepo *repository.DB, datasetID uint, index int, tagIDs map[string]uint, rng *rand.Rand) error {
	selected := make([]uint, 0, 3)

	switch {
	case index <= 106:
		selected = append(selected, tagIDs["精标完成"], tagIDs["质检通过"])
		if rng.Intn(100) < 30 {
			selected = append(selected, tagIDs["重点案件"])
		}
	case index <= 136:
		selected = append(selected, tagIDs["精标进行中"])
		if rng.Intn(100) < 40 {
			selected = append(selected, tagIDs["疑难复核"])
		}
	default:
		selected = append(selected, tagIDs["待上传"])
	}

	for _, tagID := range uniqueUint(selected) {
		if tagID == 0 {
			continue
		}
		rel := dbmodel.DatasetTag{DatasetID: datasetID, TagID: tagID}
		if err := dbRepo.DB.Create(&rel).Error; err != nil {
			return err
		}
	}

	return nil
}

func pickCaseType(i int) string {
	switch i % 3 {
	case 1:
		return "criminal"
	case 2:
		return "civil"
	default:
		return "administrative"
	}
}

func uniqueUint(values []uint) []uint {
	seen := make(map[uint]struct{}, len(values))
	res := make([]uint, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		res = append(res, v)
	}
	return res
}
