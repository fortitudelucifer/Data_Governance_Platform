package service

import (
"context"
"fmt"

"text-annotation-platform/internal/plugin"
"text-annotation-platform/internal/repository"
)

type SamplingService struct {
docRepo        repository.DocumentDB
samplingRegistry *plugin.PluginRegistry[plugin.SamplingStrategy]
}

func NewSamplingService(docRepo repository.DocumentDB, samplingRegistry *plugin.PluginRegistry[plugin.SamplingStrategy]) *SamplingService {
return &SamplingService{docRepo: docRepo, samplingRegistry: samplingRegistry}
}

func (s *SamplingService) GenerateFullPlan(ctx context.Context, datasetID uint, userID uint) ([]plugin.SegmentUnit, error) {
docs, err := s.docRepo.FindDocumentsByDataset(ctx, datasetID, nil, userID)
if err != nil {
return nil, fmt.Errorf("query documents failed: %w", err)
}
var segments []plugin.SegmentUnit
for _, doc := range docs {
paragraphs, ok := doc.Data["paragraphs"]
if !ok {
continue
}
pList, ok := paragraphs.([]interface{})
if !ok {
continue
}
for i, p := range pList {
pMap, ok := p.(map[string]interface{})
if !ok {
continue
}
text, _ := pMap["text"].(string)
segments = append(segments, plugin.SegmentUnit{DocKey: doc.DocKey, SegmentIdx: i, Text: text})
}
}
return segments, nil
}

func (s *SamplingService) GeneratePlan(ctx context.Context, datasetID uint, strategyID string, params map[string]interface{}, userID uint) ([]plugin.SegmentUnit, error) {
strategy, err := s.samplingRegistry.Get(strategyID)
if err != nil {
return nil, fmt.Errorf("unknown sampling strategy '%s': %w", strategyID, err)
}
if err := strategy.ValidateParams(params); err != nil {
return nil, err
}
allSegments, err := s.GenerateFullPlan(ctx, datasetID, userID)
if err != nil {
return nil, err
}
return strategy.Sample(allSegments, params)
}

func (s *SamplingService) ListStrategies() []plugin.StrategyInfo {
strategies := s.samplingRegistry.List()
result := make([]plugin.StrategyInfo, 0, len(strategies))
for _, st := range strategies {
result = append(result, plugin.StrategyInfo{StrategyID: st.StrategyID(), Name: st.Name()})
}
return result
}
