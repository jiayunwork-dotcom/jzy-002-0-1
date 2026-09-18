# heatx — 间壁式换热器后端核算服务

供暖通与热工工程师通过 HTTP 接口核算**间壁式换热器**工况:给定两侧质量流量、
定压比热、进口温度与热导(UA 或面积+总传热系数),指定流型,返回两侧出口温度、
实际换热量、效能、NTU、对数平均温差、运行裕度,并判定温度交叉与可行性。

纯计算服务,**不涉及设备台账、大屏或前端页面**。Go 标准库实现,零第三方依赖。

---

## 核算方法与互证

同一组进出口温度定义(能量平衡)贯穿两条**相互独立**的路径,结果必须在钉死的
容差内一致,否则服务拒绝输出(`METHOD_MISMATCH`,HTTP 500):

```
Tho = Thi − Q/Ch        Tco = Tci + Q/Cc        Ch=ṁh·cph, Cc=ṁc·cpc
```

1. **ε-NTU 法(闭式)**
   - 逆流 `Cr<1`: `ε = (1−e^{−NTU(1−Cr)}) / (1−Cr·e^{−NTU(1−Cr)})`
   - 逆流 `Cr=1`: `ε = NTU/(1+NTU)`(热容流率平衡的独立解析分支)
   - 顺流:     `ε = (1−e^{−NTU(1+Cr)}) / (1+Cr)`
   - `Cr = Cmin/Cmax ∈ (0,1]`,`NTU = UA/Cmin`
   - `Cr→1` 时用 `Expm1`/乘 `e^x` 的重写式求值,避免 `1−Cr` 处相消、除零与发散
2. **LMTD 法(独立迭代)**:以二分法解 `Q = UA·LMTD(Q)`,全局收敛、不依赖初值;
   顺流用真实可行域上界 `ΔTin/(1/Ch+1/Cc)`(出口相遇点),NTU 极大、根缩进
   端温差归零边界层时走 `LMTD=Q/UA` 的恒等稳定路径
3. 两法换热量相对偏差须 ≤ `method_agreement_rel`(默认 **1e-9**)
4. 能量闭合:`|Qh−Qc|/Qmax` ≤ `energy_closure_rel`(默认 **1e-9**)

### 物理规则

- **物理上限**:`Q ≤ Qmax = Cmin·(Thi−Tci)`,效能 `ε ≤ 1`;目标出口温度对应的
  热量达到/超过上限 → 返回 **422 TARGET_INFEASIBLE**,不假装算得出来
- **顺流不交叉**:`Tco < Tho`;`Tco ≥ Tho` 即非法。有限 UA 下闭式解恒满足;
  当 NTU 大到两侧出口在双精度下趋同(效能饱和)时,放行并显式给出饱和 warning,
  不把数值极限误判为物理交叉
- **逆流温度交叉**可发生(允许 `Tco > Tho`),结果中 `temperature_cross=true`
  并附带工程警告
- **污垢热阻**:`1/U_service = 1/U + Rf`,`UA_service = U_service·A`;
  其余不变、增大 `Rf` 时同面积下 `Q` 严格下降,逆流加热工况 `Tco` 随之降低
- **运行裕度**(给了任一目标出口温度时):
  - `duty_margin = Q_actual/Q_required − 1`
  - `ua_margin = UA_actual/UA_required − 1`,`UA_required` 由 ε-NTU **闭式反解**
    `NTU(ε_required)` 得到
  - `fouling_margin = UA_clean/UA_actual − 1`(尚可承受的进一步结垢裕度)

---

## 快速开始

### 容器一键启动(推荐)

```bash
docker compose up --build -d
# 或: make up
# 服务监听 http://localhost:8080
```

### 本地构建运行

```bash
make build          # 或: CGO_ENABLED=0 go build -o bin/heatx-server ./cmd/server
make run            # 默认 :8080;PORT=9090 make run 可改端口
```

### 测试

```bash
make test           # 全部自动化测试
make race           # 竞态检测
make cover          # 覆盖率(约 83%)
```

---

## 接口

`Content-Type: application/json`,所有响应均为 JSON。错误响应**只含 `error`、不含
`result`**,并带 `code` 与按字段定位的 `fields[].field/reason`。

### 1) 单台核算 `POST /api/v1/calculate`

请求:

```json
{
  "name": "预热器-A",
  "flow_type": "counter",
  "hot":  {"mass_flow": 2.0, "cp": 2100, "t_in": 80, "t_out_target": 63.1},
  "cold": {"mass_flow": 1.0, "cp": 2100, "t_in": 20, "t_out_target": 40},
  "area": 50, "u": 100, "fouling_r": 0.0001
}
```

热导二选一:直给 `"ua": 2100`,**或**成对给 `"area"` + `"u"`(可再附
`"fouling_r"`)。直给 `ua` 时不能再给污垢热阻(直给值即视为含污垢的服务热导)。
`t_out_target` 可选,用于算裕度;给单侧即可,给双侧则两侧目标热量必须闭合。

响应 `200`(节选):

```json
{
  "result": {
    "hot_t_out_c": 63.16, "cold_t_out_c": 53.76,
    "q_w": 71000, "q_max_w": 126000,
    "effectiveness": 0.5647, "ntu": 0.99, "capacity_ratio": 0.5,
    "lmtd_k": 33.76, "ua_w_per_k": 4950.5,
    "temperature_cross": false, "feasible": true,
    "margin": { "duty_margin": 0.02, "ua_margin": 0.31, "fouling_margin": 0.0101 },
    "checks": {
      "q_ntu_w": 71000.0, "q_lmtd_w": 71000.0,
      "method_diff_rel": 2.0e-16, "method_tolerance_rel": 1e-9,
      "energy_diff_rel": 1.2e-17, "energy_tolerance_rel": 1e-9
    },
    "warnings": []
  }
}
```

### 2) 批量核算 `POST /api/v1/calculate/batch`

```json
{ "cases": [ { ...单台入参... }, { ... } ] }
```

- **按提交顺序成表返回**,每项带 `index`(从 0 起)、`status`(`ok`/`error`)、
  `result` 或 `error`
- 某组非法时错误定位到 `cases[i].<字段>`,**其余各组照常计算**,HTTP 整体 200
- 单次最多 1000 组(`BATCH_TOO_LARGE`)

### 3) 能力/配置回显 `GET /api/v1/config`

返回支持的流型(`counter`/`parallel` 及别名)、端点、输入规格、默认容差与
批量上限。另有 `GET /healthz` 存活探针。

`flow_type` 接受:`counter`、`counterflow`、`counter-current`、`逆流`;
`parallel`、`parallelflow`、`concurrent`、`co-current`、`顺流`。

---

## 输入约束与错误码

| 约束 | 错误 code | HTTP |
|---|---|---|
| JSON 语法错误 / 空体 / 尾随内容 | `MALFORMED_JSON` | 400 |
| 未知字段、同层重复键(防参数互相覆盖) | `UNKNOWN_FIELD` / `DUPLICATE_FIELD` | 400 |
| 缺字段 / 非数值 / NaN / Inf | `MISSING_FIELD` / `INVALID_VALUE` | 400 |
| `ua`、`area`、`u` 规格冲突或不成对 | `INVALID_VALUE`(字段 `ua/area/u`) | 400 |
| `ua`/`u`/面积/流量/比热 ≤ 0,污垢 < 0 | `INVALID_VALUE` | 400 |
| 热侧进口 ≤ 冷侧进口却声称加热 | `INVALID_VALUE`(字段 `hot.t_in/cold.t_in`) | 400 |
| 两侧目标出口热量不闭合 | `CONTRADICTORY_INPUT` | 400 |
| 目标热量 ≥ Cmin·ΔTin / 顺流目标交叉 | `TARGET_INFEASIBLE` | **422** |
| ε-NTU 与 LMTD 两法超容差 / 能量不闭合 | `METHOD_MISMATCH` | 500 |
| 批量组数超上限 | `BATCH_TOO_LARGE` | 400 |

---

## 目录结构

```
cmd/server/            HTTP 服务入口(main)
internal/heatx/
  types.go             输入/输出/容差数据结构
  formulas.go          ε-NTU 闭式解、闭式反解 NTU、LMTD、LMTD 迭代
  validate.go          单台输入完整静态校验
  calc.go              核算主流程:双路互证、能量闭合、上限、交叉、裕度
  decode.go            严格 JSON 解码(未知字段/重复键/类型定位)
  batch.go             容错批量拆分(坏元素归组级错误)
  api.go               路由、错误映射、状态码、响应封装
  config.go            /api/v1/config 回显
  *_test.go            核算规则与 HTTP 层自动化测试
Dockerfile  docker-compose.yml  Makefile
```

## 默认容差

| 键 | 默认 | 含义 |
|---|---|---|
| `energy_closure_rel` | 1e-9 | 能量闭合相对容差 |
| `method_agreement_rel` | 1e-9 | ε-NTU 与 LMTD 互证相对容差 |
| `target_energy_rel` | 1e-6 | 两侧目标出口热量一致性容差 |
| `balanced_cr_threshold` | 1e-12 | `Cr→1` 走平衡分支的阈值 |
| `cross_rel_tol` | 1e-9 | 温度交叉判定的浮点死区(按 ΔTin 缩放) |

LMTD 二分固定迭代到机器精度(中点自然停滞),不使用粗糙近似或固定小迭代次数凑数。
