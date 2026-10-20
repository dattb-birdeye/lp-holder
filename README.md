# lp-holder — Liquidity Provider Holders (Solana)

Service trả về danh sách **LP holder** (người cung cấp thanh khoản) của một pool DEX trên Solana: wallet, lượng token A/B, tỷ lệ sở hữu pool và giá trị USD.

Viết bằng Go thuần (không cgo), chạy độc lập. Envelope response tương thích với `token-holder`, có thể chạy riêng hoặc nhúng vào `token-holder`.

Hỗ trợ 8 protocol, tất cả đã kiểm chứng trên mainnet ([§6](#6-kiểm-chứng-trên-mainnet)).

---

## Mục lục
1. [Bắt đầu nhanh](#1-bắt-đầu-nhanh)
2. [Kiến trúc](#2-kiến-trúc)
3. [API](#3-api)
4. [Protocol được hỗ trợ](#4-protocol-được-hỗ-trợ)
5. [Công thức tính](#5-công-thức-tính)
6. [Kiểm chứng trên mainnet](#6-kiểm-chứng-trên-mainnet)
7. [Kiểm chứng layout và sửa decoder](#7-kiểm-chứng-layout-và-sửa-decoder)
8. [Cấu hình](#8-cấu-hình)
9. [Hạn chế và hướng phát triển](#9-hạn-chế-và-hướng-phát-triển)

---

## 1. Bắt đầu nhanh

```bash
# Test (không cần mạng)
make test            # hoặc: go test -race ./...
make cover           # coverage của internal/

# Xem holder của một pool qua CLI (RPC public dùng được nhưng chậm hơn Helius)
export LP_RPC_URL=https://api.mainnet-beta.solana.com
go run ./cmd/lp-holder-cli -pool BGm1tav58oGcsQJehL9WXBFXF7D27vZsKefj4xJKD5Y -limit 5 -check
#   -check           : so sánh tổng lượng token của các holder với reserve của pool (độ phủ ~97–100% là bình thường)
#   -group-by wallet : gộp các position cùng wallet

# Chạy API
cp .env.example .env   # sửa LP_RPC_URL (Helius: ?api-key=...)
set -a; . ./.env; set +a
make run               # :8080
curl -s localhost:8080/v1/holder/liquidity-provider -d '{"address":"58oQChx4yWmvKdwLLZzBi4ChoCc2fqCUWBkwMihLYQo2","limit":5}'
```

---

## 2. Kiến trúc

```
HTTP (internal/httpapi)          CLI (cmd/lp-holder-cli)
        │                                │
        └──────────────┬─────────────────┘
                       ▼
             service.Service.Holders(query)
   ┌─ TTL cache (snapshot theo pool, giới hạn theo tổng số dòng holder)
   │    snapshot chưa đầy đủ (owner chưa resolve, position thay đổi giữa 2 lần quét) chỉ cache 15s
   ├─ negative cache (not found / unsupported / too large)
   ├─ singleflight: N request cùng pool → 1 lần tính; context tách khỏi client; recover panic
   └─ tối đa LP_MAX_CONCURRENT_COMPUTES lần tính đồng thời khi cache miss, vượt ngưỡng trả 503 "busy"
                       │ cache miss
                       ▼
  1. getAccountInfo(pool) ──► protocol.Registry.Detect(owner program, discriminator)
  2. Adapter.DecodePool            (internal/protocol, hàm thuần, test bằng fixture)
  3. enrich: 1× getMultipleAccounts[mintA, mintB, vaultA, vaultB, lpMint]
        → decimals, reserve (đã trừ fee/pnl), LP supply, giá spot
        (DAMM v1: thêm 1 call lấy LP mint của Meteora vault)
  4. holders.Strategy theo protocol (internal/holders):
        LPToken | Tick (CLMM/Whirlpool) | DAMMV2 | DLMM
  5. valuation: UI amount, % share, USD (giá từ Birdeye nếu được cấu hình)
  6. sắp xếp theo % → cache → phân trang / group_by wallet
```

```
cmd/
  lp-holder-api/     HTTP server (graceful shutdown, timeout)
  lp-holder-cli/     debug một pool, cờ -check đối soát reserve
internal/
  domain/            Pool, Holder, Snapshot, Protocol, Kind, lỗi dùng chung
  solana/            Layout (đọc theo offset), Discriminator, SPL token/mint, base58
  solana/rpc/        JSON-RPC client: retry 429/5xx kèm jitter, giới hạn concurrency,
                     gPA (+ Helius v2 paginationKey), getMultipleAccounts theo batch 100,
                     DAS getAssetBatch; thông báo lỗi không chứa URL (api-key)
  protocol/          decoder pool/position cho 8 protocol + testdata/ (account mainnet)
  poolmath/          toán CLMM, DAMM v2, DLMM, quy đổi đơn vị/giá
  holders/           strategy, resolver chủ NFT (DAS → fallback), LP source
  lpsource/          LP holder từ ClickHouse (token_holder_local)
  service/           orchestration, enrich, valuation, cache
  price/             Birdeye multi_price
  httpapi/           handler + DTO
  config/            cấu hình qua env (.env.example)
  app/               wiring
```

Phân loại pool theo **ownership model** (`domain.Kind`):
- `lp_token`: LP mint dạng fungible. Holder của LP mint được lấy qua gPA trên token program, hoặc từ index `wallet_info.token_holder_local` của `token-holder` (`internal/lpsource`, route shard theo `cityHash64(lp_mint) % N`, giống `ShardManager.GetConnByShardingKey`).
- `position`: mỗi position là một account riêng, lấy qua gPA lọc theo discriminator + memcmp pool.

**Số RPC call mỗi lần tính (cache miss)**

| Loại pool | Call |
|---|---|
| LP-token (AMM v4, CPMM, Lifinity) | 1 getAccountInfo + 1 getMultipleAccounts + 1 gPA trên token program (lọc theo mint, slice 40/165 byte). Không cần gPA nếu dùng ClickHouse |
| Meteora DAMM v1 | như trên + 1 getMultipleAccounts (LP mint của vault) |
| Raydium CLMM / Orca | 1 + 1 + 1 gPA position (slice 88/281 và 56/216 byte) + resolve chủ NFT: ⌈N/1000⌉ call DAS, hoặc fallback N× `getTokenLargestAccounts` + ⌈N/100⌉ getMultipleAccounts |
| Meteora DAMM v2 | 1 + 1 + 1 gPA (slice 160/408 byte) + resolve chủ NFT |
| Meteora DLMM | 1 + 1 + 3 gPA **song song** (owner + shares 1152/8120 byte, bin range 8 byte, bin array) + getMultipleAccounts cho position rộng hơn 70 bin. Không cần resolve owner |

---

## 3. API

`POST /v1/holder/liquidity-provider` (hỗ trợ cả `GET` với query `?address=&offset=&limit=&group_by=`) và `GET /healthz`.

Request:
```json
{ "address": "<pool>", "offset": 0, "limit": 50, "group_by": "position" }
```
- `pool_address` là alias của `address` (tương thích client cũ).
- `limit` mặc định 50, tối đa 500.
- `group_by`: `position` (mặc định, mỗi position hoặc mỗi owner LP là một dòng) hoặc `wallet` (gộp các position cùng wallet, kèm `position_count`).

Response (envelope giống `token-holder`):
```json
{
  "success": true,
  "data": {
    "pool": {
      "address": "...", "protocol": "meteora_dlmm", "kind": "position",
      "token_mint_a": "So111...", "token_mint_b": "EPjF...",
      "token_vault_a": "...", "token_vault_b": "...",
      "lp_mint": "", "lp_supply": "", "lp_mint_supply": "",
      "decimals_a": 9, "decimals_b": 6,
      "reserve_a": "22621.27", "reserve_b": "996803.19", "price_a_in_b": "104.36",
      "active_bin_id": -2261, "bin_step": 10, "tick_current": null,
      "total_value_usd": "0"
    },
    "holders": [{
      "wallet": "22Xw...", "position_address": "qgt1...",
      "liquidity": "9004523133531937916382491559358",
      "percentage": "14.4445", "token_amount_a": "3109.16", "token_amount_b": "153638.99",
      "value_usd": "0", "bin_id_lower": -2291, "bin_id_upper": -2199
    }],
    "total": 2325, "offset": 0, "limit": 50, "group_by": "position",
    "updated_at": 1792481594,
    "warnings": []
  }
}
```
- Mọi giá trị số lượng là **chuỗi thập phân** (không dùng ký hiệu mũ). `liquidity` là số nguyên u128.
- `value_usd` là `"0"` khi chưa cấu hình nguồn giá hoặc không lấy được giá; khi đó `warnings` có thông báo tương ứng.
- `warnings` mô tả các trường hợp bất thường, ví dụ: LP đã burn (Σ% < 100), không resolve được chủ NFT (wallet rỗng), phí đã bị loại khỏi reserve.

Lỗi (`{"success":false,"code","message"}`; message không chứa chi tiết nội bộ):

| HTTP | code | Điều kiện |
|---|---|---|
| 400 | `invalid_body`, `address_required`, `invalid_address`, `invalid_group_by`, `invalid_pagination`, `invalid_limit`, `invalid_offset` | input không hợp lệ |
| 404 | `pool_not_found` | account không tồn tại |
| 422 | `unsupported_pool` | không phải pool của protocol được hỗ trợ |
| 422 | `pool_too_large` | số position (hoặc số LP holder) > `LP_MAX_POSITIONS` |
| 503 | `busy` | số lần tính đồng thời đã đạt giới hạn; client retry sau |
| 502 | `upstream_error` | lỗi RPC |
| 504 | `timeout` | vượt `LP_COMPUTE_TIMEOUT` |

---

## 4. Protocol được hỗ trợ

| Protocol | Program | Kind | Nguồn holder | % share |
|---|---|---|---|---|
| Raydium AMM v4 | `675kPX…` | lp_token | holder của LP mint | `lp / AmmInfo.lp_amount` |
| Raydium CPMM | `CPMMoo…` | lp_token | holder của LP mint | `lp / PoolState.lp_supply` |
| Lifinity v2 | `2wT8Yq…` | lp_token | holder của LP mint | `lp / mint.supply` |
| Meteora DAMM v1 | `Eo7WjK…` | lp_token | holder của LP mint | `lp / mint.supply` |
| Raydium CLMM | `CAMMCz…` | position | `PersonalPositionState` → chủ NFT | theo giá trị |
| Orca Whirlpool | `whirLb…` | position | `Position` → chủ NFT (gồm cả bundle) | theo giá trị |
| Meteora DAMM v2 | `cpamdp…` | position | `Position` → chủ NFT (Token-2022) | theo liquidity (tương đương theo giá trị do cùng range) |
| Meteora DLMM | `LBUZKh…` | position | `PositionV2.owner` (+ extension khi > 70 bin) | theo giá trị |

**Lưu ý**
- Với pool LP-token, **Σ% < 100 là bình thường** khi một phần LP đã bị burn. Mẫu số là supply mà protocol dùng khi rút thanh khoản, nên phần LP đã burn không thuộc về holder nào. Ví dụ: pool CPMM `Q2sPHP…` có 92% LP bị burn; API trả warning kèm `lp_mint_supply`.
- **Lifinity v2** chủ yếu là protocol-owned liquidity: chỉ 3/48 pool có LP supply > 0 và đều rất nhỏ, nên danh sách holder thường rỗng.
- **Reserve** = số dư vault − phí không thuộc LP: AMM v4 trừ `need_take_pnl`; CPMM trừ phí protocol/fund/creator; DAMM v2 trừ phí protocol/partner. DAMM v1 tính qua vault share.

---

## 5. Công thức tính

Mọi phép tính dùng `big.Int` trên giá trị raw; chỉ chuyển sang float khi tính UI amount, % và USD.

**LP token**
```
amount_i = reserve * lp_i / lp_supply        (làm tròn xuống, giống withdraw on-chain)
```

**Raydium CLMM / Orca** (mô hình Uniswap v3, sqrt price Q64.64, L là số nguyên)
```
p = clamp(sqrtPrice, sqrt(tickLower), sqrt(tickUpper))
a = L * (sqrtUpper − p) * 2^64 / (p * sqrtUpper)
b = L * (p − sqrtLower) / 2^64
```
`tick → sqrtPrice` dùng float64 (sai số tương đối ~1e-15, khớp bản tham chiếu Python trong ngưỡng 1e-9). Để khớp chính xác từng bit cần port tick math số nguyên của protocol.

**Meteora DAMM v2** (cp-amm; **L đã scale Q64**; range là `[sqrt_min_price, sqrt_max_price]` của pool, không phải full range)
```
a = L * (sqrtMax − p) / (p * sqrtMax)
b = L * (p − sqrtMin) >> 128
```

**Meteora DLMM** (tính chính xác theo BinArray, không xấp xỉ)
```
amountX = Σ share_i * bin_i.amount_x / bin_i.liquidity_supply
amountY = Σ share_i * bin_i.amount_y / bin_i.liquidity_supply
giá bin  = (1 + binStep/10000)^binId      (bin id có dấu, quanh 0, không có offset 2^23)
```
Bin phía trên active bin chỉ chứa X, bin phía dưới chỉ chứa Y. Position rộng hơn 70 bin lưu phần share còn lại ở extension sau byte 8120 (112 byte/bin).

**% share của pool position**: `value_i / Σ value`, với `value = amountA × price_A_in_B + amountB`. Cộng trực tiếp liquidity của các range khác nhau không có ý nghĩa với CLMM/DLMM; cách này chỉ dùng làm fallback khi không tính được giá trị.

**USD**: cần giá USD của A hoặc B. Nếu chỉ có giá A thì `usd_per_B = price_A / price_A_in_B`.

---

## 6. Kiểm chứng trên mainnet

Chạy `lp-holder-cli -check` với `https://api.mainnet-beta.solana.com` (không có DAS, chủ NFT được resolve qua fallback), ngày 20/10/2026. "Độ phủ" = Σ lượng token của holder / reserve; phần chênh lệch là phí chưa claim còn trong vault hoặc LP đã burn.

| Pool | Protocol | Holder | Thời gian | Σ% | Độ phủ A / B |
|---|---|---|---|---|---|
| `58oQCh…` SOL/USDC | Raydium AMM v4 | 2139 | 1.4s | 99.99% | 99.99% / 99.99% |
| `Q2sPHP…` | Raydium CPMM | 32 | 0.3s | 8.01% (92% LP burn, có warning) | 8.01% / 8.01% |
| `BgUvEi…` SOL/USDC | Meteora DAMM v1 | 6 | 0.5s | 100% | 100% / 100% |
| `W5s3YF…` | Lifinity v2 | 0 | 0.2s | — (LP supply 0, PoL) | — |
| `5MGvNj…` | Raydium CLMM | 174 position | 2.0s | 100% | 98.88% / 97.94% |
| `HNXTRV…` SOL/… | Orca Whirlpool | 197 position | 17.7s | 100% | 99.17% / 90.64% |
| `8Pm2kZ…` SOL/USDC | Meteora DAMM v2 | 231 position | 17.4s | 100% | 91.93% / 77.32% (phần còn lại là phí chưa claim; liquidity + phí khớp 100% vault) |
| `BGm1ta…` SOL/USDC | Meteora DLMM | 2325 position | 2.5s | 100% | 99.08% / 97.41% (khớp tổng BinArray) |

Thời gian của Orca/DAMM v2 chủ yếu nằm ở bước resolve chủ NFT qua fallback trên RPC public. Với Helius DAS, mỗi 1000 NFT chỉ cần 1 call.

> ⚠️ RPC public giới hạn `getTokenLargestAccounts` rất chặt và có thể trả 429 cho mọi call sau vài lần chạy liên tiếp. Khi đó pool CLMM/Orca/DAMM v2 trả lỗi (`resolve position owners: context deadline exceeded`) thay vì trả về và cache danh sách có wallet rỗng. **Môi trường production cần endpoint hỗ trợ DAS (Helius).** RPC public chỉ dùng cho debug.

ClickHouse LP source đã chạy trên cụm local (`clickhouse-s1`); schema `token_holder_local` khớp với query.

---

## 7. Kiểm chứng layout và sửa decoder

- `internal/protocol/testdata/*.json` là **account mainnet thật** (base64), gồm pool của cả 8 protocol, position CLMM/Orca/DAMM v2/DLMM (kể cả position có extension), 2 BinArray và 1 Meteora vault.
- `expected.json` chứa giá trị tính bằng **một bản cài đặt độc lập bằng Python** (offset + công thức). Test Go phải khớp các giá trị này (DAMM v2 và DLMM khớp tuyệt đối; CLMM trong sai số 1e-9).
- Test có kiểm tra giá SOL/USDC nằm trong khoảng hợp lý (10–10000): offset sai thường làm giá lệch nhiều bậc độ lớn.
- Discriminator tính bằng `sha256("account:<Name>")[:8]`.

Quy trình thêm hoặc sửa protocol:
1. Lấy layout từ IDL/SDK chính thức (các repo `dlmm-sdk`, `damm-v2-go`, `damm-v1-sdk`).
2. Viết decoder theo **offset cố định** (`solana.Layout`), ghi chú offset, dùng `solana.Discriminator("<AccountName>")`.
3. Lấy 1–2 account thật làm fixture và thêm test.
4. Chạy `lp-holder-cli -check` trên 2–3 pool thật: Σ% ≈ 100 (pool position) và độ phủ reserve ≈ 95–100%.

---

## 8. Cấu hình

Cấu hình qua biến môi trường, xem `.env.example`. Các key chính:

| Key | Mặc định | Ghi chú |
|---|---|---|
| `LP_RPC_URL` | **bắt buộc** | Helius: `https://mainnet.helius-rpc.com/?api-key=...` |
| `LP_RPC_USE_DAS` | `true` | Tự tắt nếu endpoint không hỗ trợ (`-32601`) |
| `LP_RPC_USE_GPA_V2` | `false` | Bật khi dùng Helius cho pool rất lớn |
| `LP_RPC_MAX_CONCURRENCY` | `8` | Giới hạn số request RPC đồng thời trong process |
| `LP_MAX_POSITIONS` | `50000` | Từ chối pool quá lớn (422 `pool_too_large`) |
| `LP_CACHE_TTL` / `LP_NEGATIVE_CACHE_TTL` | `60s` / `5m` | |
| `LP_COMPUTE_TIMEOUT` | `2m` | HTTP write timeout = giá trị này + 10s |
| `LP_MAX_CONCURRENT_COMPUTES` | `4` | Giới hạn số lần tính đồng thời khi cache miss (chống quá tải khi nhiều pool khác nhau được gọi cùng lúc) |
| `LP_CACHE_MAX_HOLDERS` | `1000000` | Giới hạn bộ nhớ cache theo tổng số dòng holder (~400 B/dòng) |
| `LP_PRICE_BASE_URL`, `LP_PRICE_PATH`, `LP_PRICE_API_KEY` | trống | Bật tính USD. Public: `/defi/multi_price`; nội bộ birdeye_backend: `/public/multi_price` |
| `LP_CLICKHOUSE_SHARDS` | trống | `r1|r2,r3|r4` theo **đúng thứ tự shard** của `token-holder`. Khi có giá trị, pool LP-token đọc ClickHouse trước (timeout 10s); nếu kết quả rỗng, lỗi, hoặc vượt `LP_CLICKHOUSE_MAX_ROWS` holder thì chuyển sang RPC (không trả danh sách bị cắt) |

Integration test ClickHouse (mặc định bị skip):
```bash
LP_IT_CLICKHOUSE_SHARDS=localhost:9000 LP_IT_TOKEN=<mint> go test ./internal/lpsource -run Integration -v
```

---

## 9. Hạn chế và hướng phát triển

Theo thứ tự ưu tiên:

1. **Pool rất lớn** (Orca/Raydium SOL-USDC có 12k–20k position): tính on-demand mất nhiều giây và bước resolve owner tốn tài nguyên. Hướng xử lý:
   - Job precompute snapshot định kỳ cho top-N pool theo TVL, ghi vào ClickHouse. Schema dự kiến: `holder_stats.lp_holder_snapshot_local`, `ORDER BY (pool_address, snapshot_id, rank)`, Distributed theo `cityHash64(pool_address)`, đọc qua Distributed hoặc đúng shard, `TTL updated_at + INTERVAL 1 DAY`, chỉ đọc `snapshot_id` mới nhất.
   - API đọc snapshot trước, chỉ tính on-demand cho pool nhỏ.
2. **Cache dùng chung giữa nhiều instance** (Redis) thay cho cache in-process khi scale ngang.
   - Kèm **rate limit theo client** (ở API gateway hoặc middleware). Hiện chỉ có giới hạn số lần tính đồng thời trong process.
   - `checkSize` chạy sau khi quét xong; có thể thêm một lần gPA chỉ để đếm (`dataSlice` length 0) để từ chối pool quá lớn sớm hơn.
3. **Position bị lock / escrow** hiện hiển thị owner là PDA:
   - Raydium Burn & Earn (`LockrWmn6K5twhz3y9w1dQERbmgSaRkfnTeTKbpofwE`): owner thật nằm trong `LockedClmmPositionState` / `LockedCpLiquidityState`.
   - Meteora DAMM v1 `LockEscrow`.
   - Cần resolve sang owner thật và gắn nhãn `locked`.
4. **Gắn nhãn wallet**: burn address, PDA/program-owned, CEX, KOL (dùng lại wallet tags của `token-holder`).
5. **Phí chưa claim**: DAMM v2 có thể tính với chi phí thấp (slice đã có checkpoint + pending; pool có `fee_*_per_liquidity` ở offset 488/520; công thức `pending + L·(fee_per_liq − checkpoint) >> 128`). CLMM/DLMM cần thêm fee growth của tick/bin.
6. **Protocol mới**: PumpSwap AMM (LP mint, phổ biến), Raydium Stable. Meteora DBC là bonding curve, không có LP.
7. **Tick math số nguyên** (port từ Raydium/Orca) nếu cần khớp từng đơn vị.
8. **Locked profit của Meteora vault** (DAMM v1): hiện chưa tính, nên reserve cao hơn thực tế một chút.
9. **Token-2022 có transfer fee**: lượng hiển thị là gross.
10. **Nhúng vào `token-holder`**: cần adapter gin cho `httpapi.Handler.serve` (hoặc mount `Routes()` qua `gin.WrapH`) và dùng `ShardManager` sẵn có thay cho `lpsource.NewClickHouse`.
