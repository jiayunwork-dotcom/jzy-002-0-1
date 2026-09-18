# 间壁式换热器后端核算服务

供暖通与热工工程师通过 HTTP 接口调用的**纯计算**服务。给定换热器几何与工况
参数,返回两侧出口温度、实际换热量、效能 ε、传热单元数 NTU、对数平均温差 LMTD,
并判定温度交叉与可行性。无设备台账、无大屏、无任何前端页面。

- 语言:Go(标准库,无第三方依赖)
- 两条**独立**核算路径互证:ε-NTU 闭式公式 + LMTD 二分迭代
- 支持单台核算与批量核算(批量严格按提交顺序成表返回)
- 一条命令容器化启动:`docker compose up --build -d`

---

## 1. 构建与运行

### 本地(需 Go 1.23+)

```bash
make test          # 运行全部自动化测试
make run           # 本地启动,监听 :8080
# 或
go run ./cmd/heateserver -addr=:8080
```

### 容器(一条命令)

```bash
docker compose up --build -d     # 构建并后台启动(等价 make docker-up)
docker compose logs -f
curl -s http://localhost:8080/healthz
docker compose down
```

多架构:`Dockerfile` 使用 BuildKit 的 `TARGETARCH`,amd64 / arm64 均可原生构建。

---

## 2. 接口一览

| 方法 | 路径 | 说明 |
|------|------|------|
| GET  | `/healthz` | 存活探针 |
| GET  | `/api/v1/config` | **查询接口**:回显支持的流型与默认容差 |
| POST | `/api/v1/rate` | 单台核算 |
| POST | `/api/v1/rate/batch` | 批量核算,请求体为 JSON 数组,按顺序成表返回 |

### 单位约定

| 量 | 字段 | 单位 |
|----|------|------|
| 质量流量 | `hot.m_dot` / `cold.m_dot` | kg/s |
| 定压比热 | `hot.cp` / `cold.cp` | J/(kg·K) |
| 温度 | `*.t_in` / `*.t_out` | °C(计算只用到温差,K/°C 等价) |
| 传热面积 | `area` | m² |
| 总传热系数(洁净) | `u` | W/(m²·K) |
| 热导 | `ua` | W/K |
| 污垢热阻 | `rf` | (m²·K)/W |
| 换热量 | `q` | W |

### 几何给法(二选一,互斥)

- 直接给热导:`"ua": 5000`
- 给洁净 `u` + `area`,可附污垢:`"u": 800, "area": 10, "rf": 0.0002`
  - 含污垢总传热系数:`1/U_dirty = 1/U_clean + Rf`,`UA_dirty = U_dirty·A`
  - 直接给 `ua` 时不允许再给 `rf`(该 UA 已视为含污垢值)。

---

## 3. 请求 / 响应

### 3.1 单台核算

请求:

```bash
curl -s -X POST http://localhost:8080/api/v1/rate \
  -H 'Content-Type: application/json' \
  -d '{
    "flow": "counter",
    "hot":  {"m_dot": 2.0, "cp": 4186, "t_in": 90},
    "cold": {"m_dot": 3.0, "cp": 4186, "t_in": 20},
    "u": 800, "area": 12, "rf": 0.0002
  }'
```

响应字段:

```jsonc
{
  "ok": true,
  "result": {
    "flow": "counter",
    "hot":  {"t_out": 52.2456, "q": 316080.20, "c_rate": 8372},
    "cold": {"t_out": 45.1696, "q": 316080.20, "c_rate": 12558},
    "q": 316080.20,         // 实际换热量 W
    "ua": 8275.86,          // 用于计算的含污垢热导 W/K
    "ua_clean": 9600,       // 洁净热导 W/K
    "epsilon": 0.53935,     // 效能 ε = Q/Qmax
    "ntu": 0.98852,         // NTU = UA/Cmin
    "c_ratio": 0.66667,     // Cr = Cmin/Cmax
    "c_min": 8372, "c_max": 12558,
    "q_max": 586040,        // 物理上限 Cmin*(Th,in-Tc,in)
    "lmtd": 38.1930,        // 对数平均温差
    "temperature_cross": false,
    "feasible": true,
    "reason": "",
    "method_check": {
      "q_by_lmtd": 316080.20,     // LMTD 迭代路径
      "q_by_eps_ntu": 316080.20,  // ε-NTU 路径
      "rel_diff": 0,              // 两路径相对偏差
      "tolerance": 1e-8,
      "energy_residual": 3.6e-17, // |Qh-Qc| / Qmax
      "lmtd_iterations": 53
    }
  }
}
```

### 3.2 设计目标与运行裕度(可选)

可给一个目标(`target`),三者至多给一个,否则报参数错误:

- `"q": 200000` —— 目标换热量 W
- `"hot_t_out": 65` / `"cold_t_out": 40` —— 目标某侧出口温度 °C

返回中额外给出:

- `target_q`:换算后的目标换热量
- `margin`:**运行裕度** = `Q实际/Q目标 − 1`
- `feasible=false` + `reason` 当目标超过物理上限、或实际能力不足、或顺流交叉。

### 3.3 批量核算

请求体是数组;传输层恒为 `200`,逐组给结论,**某组非法不影响其余组**,
并标明是第几组、哪个参数:

```bash
curl -s -X POST http://localhost:8080/api/v1/rate/batch \
  -H 'Content-Type: application/json' \
  -d '[
    {"flow":"counter","hot":{"m_dot":1,"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":1},
    {"flow":"counter","hot":{"m_dot":1,"cp":1,"t_in":100},"cold":{"m_dot":1,"cp":1,"t_in":0},"ua":-5},
    {"flow":"parallel","hot":{"m_dot":2,"cp":1,"t_in":120},"cold":{"m_dot":1,"cp":1,"t_in":20},"u":400,"area":2}
  ]'
```

```jsonc
{
  "ok": true,
  "rows": [
    {"index": 1, "ok": true, "result": { /* ... */ }},
    {"index": 2, "ok": false,
     "error": {"message": "第 2 组参数 ua: 热导 UA 必须为正,收到 -5",
               "problems": [{"index": 2, "field": "ua", "issue": "热导 UA 必须为正,收到 -5"}]}},
    {"index": 3, "ok": true, "result": { /* ... */ }}
  ]
}
```

### 3.4 配置查询

`GET /api/v1/config` 回显支持的流型(`counter`/`parallel`,兼容 `逆流`/`顺流` 等
别名)、默认容差与全部核算规则。

---

## 4. 核算规则(都钉死并有自动化测试)

1. **能量闭合**:热侧放热 = 冷侧吸热,忽略散热。残差
   `|Qh−Qc|/Qmax ≤ 1e-9`。
2. **两路径互证**:ε-NTU 闭式公式与 LMTD 二分迭代**共用同一套进出口温度定义**;
   两条路径各自解出的换热量相对偏差 `≤ 1e-8`;非极限工况还须满足
   `UA·LMTD = Q`。
3. **效能闭式公式**
   - 顺流:`ε = [1 − exp(−NTU(1+Cr))] / (1+Cr)`
   - 逆流:`ε = [1 − exp(−NTU(1−Cr))] / [1 − Cr·exp(−NTU(1−Cr))]`
   - **平衡极限** `Cr → 1`:`ε = NTU/(1+NTU)`;当 `NTU·(1−Cr) < 1e-8` 时
     自动走极限公式,杜绝 0/0 相消与发散(不依赖 `Cr==1` 的精确相等)。
4. **热容比** `Cr = Cmin/Cmax ∈ (0,1]`,`C = m_dot·cp`。
5. **物理上限** `Qmax = Cmin·(Th,in − Tc,in)`,`0 < ε ≤ 1`,`Q ≤ Qmax`。
   用户目标(换热量或目标出口温度)对应热量超过上限时返回
   `feasible=false` 并说明,绝不编造结果。
6. **顺流不交叉**:顺流冷侧出口必须严格低于热侧出口;`Tc,out ≥ Th,out` 即非法。
   逆流温度交叉合法,仅以 `temperature_cross=true` 报告。UA 极大时顺流会贴到
   夹点极限,同样判为不可行并说明。
7. **污垢单调性**:`1/U_dirty = 1/U_clean + Rf`。面积不变、`Rf` 增大时
   `UA↓ ⇒ Q↓`,逆流加热工况冷侧出口温度随之降低。
8. **LMTD 数值稳定**:端点温差接近时用 `dMean·u/atanh(u)`(u 小时接 Taylor 级数),
   不用粗糙近似;LMTD 路径用 100 次二分求根,不发散、不需初值。

---

## 5. 输入校验与异常

- 热导 / `U` / 面积、质量流量、比热必须为**正有限数**;污垢热阻不得为负。
- 热侧进口不高于冷侧进口却声称加热 → 明确拒绝(指出 `hot.t_in`)。
- 缺字段、非数值、`NaN/Inf`、未知字段、**同层重复键**(防止参数互相覆盖)、
  多个 JSON 值 → `400`,错误信息点名参数;**不返回 result,也不会崩溃**。
- 单台:`400` + `{"ok":false,"error":{...}}`。
- 批量:传输层 `200`,错误落在对应行并带 `index`;数组本身非法才整体 `400`。

错误响应示例:

```json
{ "ok": false,
  "error": { "message": "参数 hot.t_in: 热侧进口 20 °C 不高于冷侧进口 80 °C,与加热(热->冷)工况矛盾",
             "problems": [{"field":"hot.t_in","issue":"..."}] } }
```

---

## 6. 测试

`go test ./...`(或 `make test`)覆盖:

- 能量闭合(多 Cr、多 NTU、两流型扫描)
- ε-NTU 与 LMTD 两路径互证
- 污垢增大 ⇒ 换热量下降、冷侧出口下降
- 目标超过物理上限 ⇒ 不可行;能力不足 ⇒ 负裕度;达标 ⇒ 正裕度
- 顺流出口不交叉、夹点极限;逆流交叉合法
- Cr 趋近 1(含 `1±1e-12`)不除零、不发散
- LMTD 近相等温差稳定形式
- 各类非法输入(缺字段/非数值/互斥参数/矛盾工况/重复键/未知字段)
- HTTP:配置回显、批量顺序与逐组错误定位、严格 JSON
