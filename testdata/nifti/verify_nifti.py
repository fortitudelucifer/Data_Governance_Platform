#!/usr/bin/env python
"""C3.4 NIfTI 导出的**第三方权威验证**（nibabel）。

为什么需要这个脚本
------------------
后端有 NIfTI 的写入器（nifti_write.go）和读取器（volume_probe.go）。
让写入器写、读取器读，往返永远成功——**哪怕两边共享同一个 bug**。
C3.3a 的 COCO RLE 就栽在这上面：自洽的编码器往返完美，直到导出到 3D Slicer
才发现全是错的。所以几何的权威解释权交给 nibabel（NIfTI 事实标准实现）。

它做两件事
----------
1. 用 Go 写出一个"刻意不好看"的体（各向异性 + 非零原点 + 左手系 LAS 朝向），
   用 nibabel 读回来，逐项比对 affine / 体素 / 数据类型。
   正交单位阵测不出任何朝向 bug——它对任何写法都成立，所以夹具必须别扭。
2. 比对通过后，把这一份冻进 golden.nii + golden.json，
   让 Go 单测在 CI 里对着夹具比（CI 不必装 Python，权威性来自生成时这一次比对）。

用法：
    python testdata/nifti/verify_nifti.py           # 验证并（首次）生成夹具
    python testdata/nifti/verify_nifti.py --regen   # 强制重生成（改了格式才用）
"""
from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import warnings

import nibabel as nib
import numpy as np

HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parent.parent
BACKEND = REPO / "backend"

# 与 Go 侧 awkwardMeta() 必须一致。别"顺手改整齐"——别扭正是它的价值。
DIMS = (4, 3, 2)
SPACING = (0.7, 1.25, 3.0)
ORIGIN = (-90.5, 12.25, -7.75)
DIRECTION = [-1, 0, 0, 0, 1, 0, 0, 0, 1]  # i→-x：左右翻转，det<0（左手系）

GO_GEN = r'''
package main

import (
	"encoding/json"
	"os"

	"text-annotation-platform/internal/service"
)

func main() {
	var spec struct {
		Dims     [3]int      `json:"dims"`
		Affine   [][]float64 `json:"affine"`
		Labels   []uint16    `json:"labels"`
		MaxLabel int         `json:"max_label"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&spec); err != nil {
		panic(err)
	}
	var aff [3][4]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 4; c++ {
			aff[r][c] = spec.Affine[r][c]
		}
	}
	gz, err := service.WriteNIfTIGz(service.NIfTIVolume{
		Dims: spec.Dims, Affine: aff, Labels: spec.Labels,
		MaxLabel: spec.MaxLabel, Descrip: "dg-seg",
	})
	if err != nil {
		panic(err)
	}
	if _, err := os.Stdout.Write(gz); err != nil {
		panic(err)
	}
}
'''


def affine_from_parts() -> list[list[float]]:
    """direction（归一化列，行主序）× spacing + origin → 3×4 voxel→world。"""
    return [
        [DIRECTION[r * 3 + c] * SPACING[c] for c in range(3)] + [ORIGIN[r]]
        for r in range(3)
    ]


def run_go_writer(spec: dict) -> bytes:
    """把 spec 喂给一次性 Go 程序，拿回 .nii.gz 字节。

    ⚠️ 临时程序必须落在 **backend 模块树内**：Go 的 `internal/` 只允许同模块
    导入，放系统临时目录会报 "use of internal package not allowed"（踩过）。
    用完即删，不留在仓库里。
    """
    tmpdir = BACKEND / ".tmp_niftigen"
    tmpdir.mkdir(exist_ok=True)
    try:
        (tmpdir / "main.go").write_text(GO_GEN, encoding="utf-8")
        proc = subprocess.run(
            ["go", "run", "./.tmp_niftigen"],
            input=json.dumps(spec).encode(),
            capture_output=True,
            cwd=BACKEND,
        )
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)
    if proc.returncode != 0:
        sys.exit(f"go run 失败:\n{proc.stderr.decode(errors='replace')}")
    return proc.stdout


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--regen", action="store_true", help="强制重生成夹具")
    args = ap.parse_args()

    n = DIMS[0] * DIMS[1] * DIMS[2]
    labels = [i % 3 for i in range(n)]
    affine = affine_from_parts()
    spec = {"dims": list(DIMS), "affine": affine, "labels": labels, "max_label": 2}

    print("→ 调用 Go 写入器…")
    gz = run_go_writer(spec)

    with tempfile.TemporaryDirectory() as td:
        p = pathlib.Path(td) / "seg.nii.gz"
        p.write_bytes(gz)

        # ⚠️ nibabel 的告警必须当**失败**处理，不能只打印。
        # 第一次跑时它就报了 "pixdim[1,2,3] should be non-zero"——
        # 那是 pixdim 写偏了一个字段（pixdim[1] 在偏移 80，不是 84），
        # 而我们自己的 parser 只读 sform、根本不看 pixdim，往返测试完美通过。
        # 告警只是滚过屏幕的话，这个 bug 就会连同夹具一起被冻进仓库。
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            img = nib.load(str(p))
            got_affine = np.asarray(img.affine, dtype=float)
            data = np.asarray(img.dataobj)
            hdr = img.header

        print("→ nibabel 读回，逐项比对…")
        ok = True

        if caught:
            ok = False
            for w in caught:
                print(f"  ✗ nibabel 告警（视为失败）：{w.message}")
        else:
            print("  ✓ nibabel 读取无任何告警")

        want_affine = np.array(affine + [[0, 0, 0, 1]], dtype=float)
        if not np.allclose(got_affine, want_affine, atol=1e-5):
            ok = False
            print(f"  ✗ affine 不符\n    got:\n{got_affine}\n    want:\n{want_affine}")
        else:
            print("  ✓ affine 一致（含非零原点与负向第一列）")

        # 体素排布：NIfTI 是 Fortran 序（i 最快）。搞反了整个体会被转置，
        # 而转置后的体**看起来仍然像个正常的分割**——这正是最危险的错法。
        want_vox = np.asarray(labels, dtype=np.uint8).reshape(DIMS, order="F")
        if not np.array_equal(data, want_vox):
            ok = False
            print(f"  ✗ 体素排布不符（是不是 C 序 / Fortran 序搞反了？）")
            print(f"    got[:, 0, 0] = {data[:, 0, 0]}, want = {want_vox[:, 0, 0]}")
        else:
            print("  ✓ 体素逐个一致（Fortran 序正确）")

        if hdr.get_data_dtype() != np.uint8:
            ok = False
            print(f"  ✗ 数据类型 {hdr.get_data_dtype()}，期望 uint8")
        else:
            print("  ✓ 数据类型 uint8")

        # 手性：det<0 = 左手系。这一位翻了就是左右反了，而左右反了肉眼看不出来。
        det = float(np.linalg.det(got_affine[:3, :3]))
        if det >= 0:
            ok = False
            print(f"  ✗ 行列式 {det:+.4f}，期望为负（左手系被翻正了 = 左右反了）")
        else:
            print(f"  ✓ 手性保持（det = {det:+.4f}）")

        # nibabel 对朝向的字母判读——最直观的"左右对不对"。
        axcodes = nib.aff2axcodes(got_affine)
        print(f"  ✓ nibabel 判读朝向 = {axcodes}（LAS：第一轴指向 L）")
        if axcodes[0] != "L":
            ok = False
            print("  ✗ 第一轴不是 L —— 左右翻转了")

        sform_code = int(hdr["sform_code"])
        qform_code = int(hdr["qform_code"])
        if sform_code != 1 or qform_code != 0:
            ok = False
            print(f"  ✗ sform_code={sform_code} qform_code={qform_code}，期望 1 / 0")
        else:
            print("  ✓ 只写 sform，不写 qform（两者不一致是 L/R 翻转的经典来源）")

        if not ok:
            print("\n验证失败——**不要**重生成夹具来让它变绿，先弄清哪边错了。")
            return 1

    # 冻结夹具：存**未压缩**的 .nii（gzip 参数会变，载荷才是契约）。
    import gzip as _gzip

    raw = _gzip.decompress(gz)
    golden_nii = HERE / "golden.nii"
    golden_json = HERE / "golden.json"

    if golden_nii.exists() and not args.regen:
        if golden_nii.read_bytes() == raw:
            print("\n✓ 全部通过，且与既有夹具逐字节一致。")
            return 0
        print("\n⚠ 验证通过，但与既有夹具**不一致**。")
        print("  这说明写入器的输出变了。若是有意变更，用 --regen 重生成；")
        print("  否则先查清为什么变了（几何/编码的改动都会在这里显形）。")
        return 1

    golden_nii.write_bytes(raw)
    golden_json.write_text(
        json.dumps(
            {
                "_comment": "由 verify_nifti.py 生成并经 nibabel 校验。别手改。",
                "dims": list(DIMS),
                "spacing": list(SPACING),
                "origin": list(ORIGIN),
                "direction": DIRECTION,
                "affine": affine,
                "labels": labels,
                "max_label": 2,
                "sha256_nii": hashlib.sha256(raw).hexdigest(),
                "verified_with": f"nibabel {nib.__version__}",
            },
            ensure_ascii=False,
            indent=2,
        ),
        encoding="utf-8",
    )
    print(f"\n✓ 全部通过。夹具已写入：\n  {golden_nii}\n  {golden_json}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
