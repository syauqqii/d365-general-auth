# general-auth

Backend autentikasi + gateway antara aplikasi **web / mobile** dan **Dynamics 365 Business Central (BC) / Finance & Operations (FO)**.

Aplikasi tidak pernah berbicara langsung dengan D365 dan tidak pernah memegang client secret Microsoft Entra.
Alurnya: login ke backend ini, lalu setiap request data D365 lewat backend ini. Backend mengecek siapa usernya,
mengecek role-nya di `rbac.toml`, mengambil token Microsoft Entra miliknya sendiri, lalu meneruskan request ke
endpoint D365 yang sudah di-mapping di `connector-*.toml`.

```
 Web / Mobile ──(access token)──▶ general-auth ──▶ cek session + role (rbac.toml)
                                       │
                                       └──▶ token Microsoft Entra (di-cache) ──▶ D365 BC / FO (OData)
```

**Stack:** Go 1.25, [Fiber v2](https://gofiber.io) (HTTP), [sqlx](https://github.com/jmoiron/sqlx) (lapisan tipis di atas `database/sql`, tanpa ORM berat),
golang-jwt v5, bcrypt. Database: **PostgreSQL, MySQL/MariaDB, SQL Server, SQLite**. Pilih di config, tanpa ubah kode.

---

## Daftar isi

1. [Instalasi](#1-instalasi)
2. [Setup langkah demi langkah](#2-setup-langkah-demi-langkah)
3. [Seeder user](#3-seeder-user)
4. [Menjalankan](#4-menjalankan)
5. [Setup D365 (BC & FO)](#5-setup-d365-bc--fo)
6. [Konsep: token, session, web vs mobile](#6-konsep-token-session-web-vs-mobile)
7. [Panduan integrasi client](#7-panduan-integrasi-client)
8. [Referensi API](#8-referensi-api)
9. [RBAC dan proteksi user](#9-rbac-dan-proteksi-user)
10. [Referensi konfigurasi](#10-referensi-konfigurasi)
11. [Keamanan dan checklist produksi](#11-keamanan-dan-checklist-produksi)
12. [Struktur project & troubleshooting](#12-struktur-project)

---

## 1. Instalasi

Prasyarat:

- **Go 1.25+**: `go version`
- Salah satu database: PostgreSQL 13+, MySQL 8 / MariaDB 10.5+, SQL Server 2017+, atau SQLite (cukup untuk development; tidak perlu install apa pun)
- `openssl` (opsional) untuk generate secret

```bash
git clone <repo> general-auth
cd general-auth
go mod download
go build -o general-auth .      # Windows: go build -o general-auth.exe .
```

Semua perintah di bawah memakai binary `./general-auth`. Selama development bisa diganti dengan `go run .`.

---

## 2. Setup langkah demi langkah

Setiap file config punya pasangan `example.*`. File asli (`config.toml`, `.env`, `rbac.toml`, `connector-*.toml`, `init/users.json`)
**tidak di-commit** (ada di `.gitignore`) karena berisi setting per environment dan secret.

### Langkah 1: copy file contoh

```bash
cp example.env               .env
cp example.config.toml       config.toml
cp example.rbac.toml         rbac.toml
cp example.connector-bc.toml connector-bc.toml   # kalau pakai Business Central
cp example.connector-fo.toml connector-fo.toml   # kalau pakai Finance & Operations
cp init/users.example.json   init/users.json
```

Kalau hanya memakai salah satu dari BC/FO, hapus connector yang tidak dipakai dari `[connectors]` di `config.toml`.

### Langkah 2: isi `.env` (secret)

```bash
openssl rand -hex 32   # jalankan 2x: satu untuk JWT_ACCESS_SECRET, satu untuk JWT_REFRESH_SECRET
```

| Variabel | Isi |
|---|---|
| `JWT_ACCESS_SECRET`, `JWT_REFRESH_SECRET` | dua secret acak **berbeda**, minimal 32 karakter |
| `DB_*` | koneksi database (lihat langkah 3) |
| `BC_*` / `FO_*` | kredensial app Microsoft Entra (lihat [bagian 5](#5-setup-d365-bc--fo)) |

`config.toml` hanya berisi referensi `${NAMA_VAR}` ke `.env`. Env asli dari OS/container selalu menang atas `.env`,
jadi di server cukup set environment variable tanpa file `.env`.

### Langkah 3: pilih database

Atur di `.env` (atau langsung di `[database]` pada `config.toml`):

| Database | `DB_DRIVER` | `DB_PORT` | Catatan |
|---|---|---|---|
| PostgreSQL | `postgres` | 5432 | `DB_SSLMODE`: `disable` (lokal) / `require` / `verify-full` (produksi) |
| MySQL / MariaDB | `mysql` | 3306 | `DB_TLS`: `false` / `true` / `skip-verify` |
| SQL Server | `sqlserver` | 1433 | `DB_ENCRYPT=true`, `DB_TRUST_SERVER_CERT=false` |
| SQLite | `sqlite` | - | `DB_NAME=data/general-auth.db` (path file; folder harus sudah ada) |

Database (schema) harus sudah dibuat. Tabel dibuat otomatis oleh migrasi:

```sql
CREATE DATABASE general_auth;   -- Postgres/MySQL/SQL Server
```

Parameter tambahan per driver ada di `[database.params.<driver>]`. Hanya tabel milik driver aktif yang dipakai.

### Langkah 4: sesuaikan `config.toml`

Yang biasanya perlu diubah:

- `[app] cors_origins`: origin web app kamu, misalnya `["https://portal.perusahaan.com"]`. Mobile tidak butuh CORS.
- `[jwt.access] ttl`, `[jwt.refresh] web_ttl / mobile_ttl / max_session_age`: umur token (lihat [bagian 6](#6-konsep-token-session-web-vs-mobile)).
- `[security]`: rate limit dan lockout.
- `[account]`: default boleh-tidaknya user mengganti username/email/password sendiri.

### Langkah 5: atur role di `rbac.toml`

Default contoh: semua user login boleh akses semua route non-admin. Entity D365 dibatasi per role, dan delete hanya untuk superadmin.
Detail di [bagian 9](#9-rbac-dan-proteksi-user).

### Langkah 6: validasi

```bash
./general-auth check
# config OK: env=development db=postgres connectors=2
```

`check` membaca `config.toml`, `rbac.toml`, dan semua connector, lalu melaporkan setiap kesalahan (key salah ketik, secret terlalu pendek, dll.).

---

## 3. Seeder user

Seeder membuat user awal dari `init/users.json`. Kalau file itu belum ada, seeder **berhenti** dan meminta kamu membuatnya dulu:

```
seed file .../init/users.json not found.
Create it first (copy init/users.example.json to .../init/users.json and edit the users), then run the seeder again.
```

```bash
cp init/users.example.json init/users.json   # lalu edit
./general-auth seed            # buat user yang belum ada, skip yang sudah ada (berdasarkan username)
./general-auth seed -update    # timpa user yang sudah ada dengan isi file
```

Seeder otomatis menjalankan migrasi dulu. Format file:

```json
[
  {
    "username": "superadmin",
    "email": "superadmin@perusahaan.com",
    "name": "Super Administrator",
    "password": "Ganti-Password-Kuat-123",
    "role": "superadmin",
    "can_change_username": false,
    "can_change_email": false,
    "can_change_password": true
  },
  {
    "username": "sales01",
    "code": "S001",
    "name": "Sales One",
    "password_hash": "$2a$12$....",
    "role": "sales",
    "is_active": true
  }
]
```

| Field | Wajib | Keterangan |
|---|---|---|
| `username` | ya | 3-100 karakter `a-z 0-9 . _ -`, disimpan lowercase |
| `email`, `code` | tidak | unik; `code` = sales id / employee id / kode lain untuk login |
| `password` / `password_hash` | salah satu | plain (di-hash saat seed) atau bcrypt (`./general-auth hash-password 'rahasia'`) |
| `role` | ya | harus ada di `roles` pada `rbac.toml` |
| `is_active` | tidak | default `true` |
| `can_change_username/email/password` | tidak | default dari `[account]` di config |

Tips:

- **Superadmin hanya bisa dibuat atau diubah lewat seeder/CLI**, tidak lewat API. Ini disengaja (lihat [bagian 9](#9-rbac-dan-proteksi-user)).
- Lebih aman pakai `password_hash` supaya file tidak menyimpan password plain. Hapus `init/users.json` setelah seeding di server produksi.
- `seed -update` dengan field `password` akan mengganti password **dan** me-logout user tersebut di semua device.

---

## 4. Menjalankan

```bash
./general-auth migrate   # opsional; serve juga migrate otomatis kalau auto_migrate = true
./general-auth serve     # atau cukup ./general-auth
curl localhost:3000/health
```

| Command | Fungsi |
|---|---|
| `serve` (default) | jalankan API |
| `migrate` | jalankan migrasi database berversi (`schema_migrations`) |
| `seed [-update]` | isi user dari `init/users.json` |
| `check` | validasi semua file config lalu keluar |
| `hash-password <pass>` | cetak hash bcrypt untuk `password_hash` |

Semua command menerima `-config path/ke/config.toml` (default `config.toml`, atau env `CONFIG_PATH`).
Server berhenti dengan rapi saat menerima `SIGINT`/`SIGTERM`.

---

## 5. Setup D365 (BC & FO)

Keduanya memakai **OAuth 2.0 client credentials** (service-to-service) ke Microsoft Entra ID:

```
POST https://login.microsoftonline.com/<tenant-id>/oauth2/v2.0/token
grant_type=client_credentials & client_id=... & client_secret=... & scope=<lihat tabel>
```

| | Business Central | Finance & Operations |
|---|---|---|
| `d365_scope` | `https://api.businesscentral.dynamics.com/.default` | `https://<env>.operations.dynamics.com/.default` |
| `api_url` (OData) | `https://api.businesscentral.dynamics.com/v2.0/<tenant>/<environment>/ODataV4/Company('<company>')/%s` atau API v2.0: `.../api/v2.0/companies(<id>)/%s` | `https://<env>.operations.dynamics.com/data/%s` |
| Contoh entity | `Customers_Card` (web service), `customers` (API v2.0) | `CustomersV3`, `ReleasedProductsV2`, `SalesOrderHeadersV2`, `SalesOrderLines` |
| Contoh key | `Customers_Card('10000')` | `CustomersV3(dataAreaId='usmf',CustomerAccount='US-001')` |

### Business Central

1. **Microsoft Entra admin center** > App registrations > New registration.
2. *Certificates & secrets* > New client secret, lalu salin value-nya ke `BC_CLIENT_SECRET`.
3. *API permissions* > Dynamics 365 Business Central > **Application permissions** > `API.ReadWrite.All` > Grant admin consent.
4. Di BC, buka halaman **Microsoft Entra Applications** > New: isi Client ID, set State = **Enabled**, lalu assign permission set seminimal mungkin (SUPER tidak bisa di-assign ke app).
5. Untuk OData web services, publish page-nya di halaman **Web Services**. Nama service itulah yang ditulis di `[endpoints]`.

Sumber: [Using Service to Service Authentication (BC)](https://learn.microsoft.com/dynamics365/business-central/dev-itpro/administration/automation-apis-using-s2s-authentication)

### Finance & Operations

1. **Microsoft Entra admin center** > App registrations > New registration, lalu buat client secret.
2. Di FO: **System administration > Setup > Microsoft Entra applications** > New: isi Client ID dan **User ID**.
   Pakai user service khusus yang hanya punya security role yang dibutuhkan (bukan Admin), karena semua call memakai hak akses user ini.
3. `FO_ENV_HOST` = host environment tanpa `https://` dan tanpa slash, misalnya `contoso.operations.dynamics.com`.
4. Daftar entity set publik ada di `https://<env>.operations.dynamics.com/data/$metadata`.

Secara default FO hanya mengembalikan data **company default** milik user service. `?cross-company=true` membuka semua company
yang bisa diakses user itu. Contoh connector FO memaksa `cross-company=false` lewat `[forced_query]` supaya client tidak bisa
membaca legal entity lain.

Sumber: [Service endpoints overview (FO)](https://learn.microsoft.com/dynamics365/fin-ops-core/dev-itpro/data-entities/services-home-page), [OData (FO)](https://learn.microsoft.com/dynamics365/fin-ops-core/dev-itpro/data-entities/odata)

### Connector file

```toml
d365_product = "fo"
d365_tenant_id = "${FO_TENANT_ID}"
d365_client_id = "${FO_CLIENT_ID}"
d365_client_secret = "${FO_CLIENT_SECRET}"
d365_grant_type = "client_credentials"
d365_scope = "https://${FO_ENV_HOST}/.default"
d365_auth_url = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"
api_url = "https://${FO_ENV_HOST}/data/%s"
timeout = "60s"

[endpoints]          # nama generik di URL API = nama entity set di D365
customer = "CustomersV3"

[default_query]      # ditambahkan kalau client tidak mengirim parameter ini
"$top" = "200"

[forced_query]       # selalu dipakai, menimpa nilai dari client
cross-company = "false"
```

Menambah entity cukup dengan menambah baris di `[endpoints]` lalu restart, tanpa ubah kode.
Menambah environment (misalnya `bc_prod`): buat file connector baru, lalu daftarkan di `[connectors]` pada `config.toml`.

Token Entra di-cache di memory per connector sampai ±1 menit sebelum expired. Kalau D365 membalas 401, token diambil ulang lalu request di-retry sekali.

---

## 6. Konsep: token, session, web vs mobile

### Satu API untuk web dan mobile

Semua client memakai **endpoint yang sama** (`/api/v1/...`) dan **jenis token yang sama**: access token + refresh token.
Kenapa tidak dipisah per prefix (`/api/web`, `/api/mobile`)?

- Logika bisnis, RBAC, dan audit cukup satu. Endpoint ganda gampang tidak sinkron (misalnya fitur ditambah di web tapi lupa di mobile).
- Perbedaan web dan mobile sebenarnya hanya **cara menyimpan refresh token** dan **umurnya**, bukan API-nya.

Client menyebutkan jenisnya saat login (`"client": "web"` atau `"mobile"`, atau header `X-Client-Type`). Default-nya `web`.

| | Web (browser) | Mobile |
|---|---|---|
| Access token | body JSON, simpan **di memory** (bukan localStorage) | body JSON, simpan di memory |
| Refresh token | **cookie HttpOnly + Secure + SameSite=Strict**, tidak terlihat oleh JavaScript | body JSON, simpan di **Keychain (iOS) / Keystore (Android)** |
| Umur refresh (idle) | `jwt.refresh.web_ttl` (default 7 hari) | `jwt.refresh.mobile_ttl` (default 90 hari) |
| Umur maksimum session | `jwt.refresh.max_session_age` (default 180 hari), setelah itu wajib login ulang | sama |

Kenapa mobile tetap pakai refresh token: access token sengaja pendek (15 menit). Kalau bocor (log, proxy, crash report),
token itu cepat tidak berguna. Refresh token yang umurnya panjang hanya dikirim ke satu endpoint, dirotasi setiap dipakai,
dan disimpan di storage aman milik OS. Untuk user, efeknya sama dengan "login lama": selama app dipakai minimal sekali dalam
`mobile_ttl`, user tidak perlu login ulang.

### Session

Setiap login membuat satu baris **session** (satu per device). Semua token membawa ID session, dan setiap request mengecek
session + status user di database (satu query by primary key). Akibatnya:

- **Logout, deactivate, ganti password, atau force-logout oleh admin langsung berlaku** di web dan mobile, tanpa menunggu token expired.
- Perubahan role langsung berlaku karena role dibaca dari DB, bukan dari token.
- **Rotasi refresh token**: setiap `/auth/refresh` menerbitkan refresh token baru, dan yang lama tidak berlaku lagi.
  Kalau refresh token lama dipakai lagi (tanda token dicuri), **seluruh session itu dicabut** dan user harus login ulang.

---

## 7. Panduan integrasi client

### Web (SPA)

```js
// login
const r = await fetch(`${API}/api/v1/auth/login`, {
  method: "POST",
  credentials: "include",                       // supaya cookie refresh diterima/dikirim
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ identifier, password, client: "web" }),
});
const { data } = await r.json();
let accessToken = data.access_token;            // simpan di memory saja

// panggil API
fetch(`${API}/api/v1/me`, { headers: { Authorization: `Bearer ${accessToken}` } });

// saat dapat 401 (atau sebelum expires_at): refresh
const rr = await fetch(`${API}/api/v1/auth/refresh`, {
  method: "POST",
  credentials: "include",
  headers: { "X-Requested-With": "fetch" },     // WAJIB saat memakai cookie (proteksi CSRF)
});
```

- Saat halaman di-reload, panggil `/auth/refresh` dulu untuk mendapatkan access token baru dari cookie.
- Kalau web dan API beda origin, masukkan origin web ke `cors_origins` (tidak boleh `"*"`) dan pakai `credentials: "include"`.
  Kalau beda *site* (bukan sekadar beda subdomain), cookie `SameSite=Strict` tidak akan terkirim. Pilihan terbaik:
  taruh API di subdomain yang sama (`api.perusahaan.com` + `app.perusahaan.com`), atau set `same_site = "None"` (wajib `secure = true`).

### Mobile

```http
POST /api/v1/auth/login
{ "identifier": "S001", "password": "...", "client": "mobile" }
→ { access_token, expires_at, refresh_token, refresh_expires_at, user }
```

- Simpan `refresh_token` di Keychain/Keystore, `access_token` di memory.
- Saat `401` dengan `error.code = "invalid_token"`, atau menjelang `expires_at`, panggil `POST /api/v1/auth/refresh` dengan body
  `{ "refresh_token": "..." }`. **Selalu simpan refresh token yang baru**, karena yang lama langsung tidak berlaku.
- Pastikan hanya ada **satu** proses refresh pada satu waktu (pakai mutex/queue). Dua refresh paralel dengan token yang sama
  dianggap reuse, dan session-nya dicabut.
- Saat `401` dengan kode `session_ended` / `refresh_token_reused`, atau `403 user_inactive`, arahkan user ke layar login.
- Logout: `POST /api/v1/auth/logout` dengan body `{ "refresh_token": "..." }` (tetap berhasil walaupun access token sudah expired).

---

## 8. Referensi API

Base path: `/api/v1`. Response: `{"success": true, "data": ...}` atau `{"success": false, "error": {"code", "message"}}`.
Pengecualian: proxy D365 meneruskan body D365 apa adanya. 🔒 = butuh `Authorization: Bearer <access_token>`.

### Auth

| Method | Path | Body / Keterangan |
|---|---|---|
| POST | `/auth/login` | `{identifier, password, client?, login_type?}`. `identifier` bisa username, email, atau code. `login_type`: `auto` (default), `username`, `email`, `code`. Bisa juga `{username, password}` / `{email, ...}` / `{code, ...}` |
| POST | `/auth/refresh` | mobile: `{refresh_token}`; web: dari cookie + header `X-Requested-With` |
| POST | `/auth/logout` | access token dan/atau refresh token (body/cookie); cabut session ini |
| POST | `/auth/logout-all` 🔒 | cabut semua session user ini di semua device |

### Akun sendiri (settings user)

Semua perubahan wajib menyertakan `current_password` **dan** butuh flag izin user (`can_change_*`).
Password salah dihitung ke lockout yang sama dengan login.

| Method | Path | Body |
|---|---|---|
| GET | `/me` 🔒 | data user + session saat ini |
| PUT | `/me/password` 🔒 | `{current_password, new_password}`; device lain otomatis logout |
| PUT | `/me/email` 🔒 | `{current_password, email}` |
| PUT | `/me/username` 🔒 | `{current_password, username}` |
| GET | `/me/sessions` 🔒 | daftar device yang sedang login |
| DELETE | `/me/sessions/:id` 🔒 | logout satu device |

### Admin (default: `admin_roles`)

| Method | Path | Keterangan |
|---|---|---|
| GET | `/admin/users?q=&role=&active=&limit=&offset=` 🔒 | list + cari user |
| POST | `/admin/users` 🔒 | buat user (body sama seperti `users.json`) |
| GET | `/admin/users/:id` 🔒 | detail |
| PATCH | `/admin/users/:id` 🔒 | ubah sebagian: `name, email, code, username, role, password, is_active, can_change_username, can_change_email, can_change_password` |
| POST | `/admin/users/:id/activate` 🔒 | aktifkan |
| POST | `/admin/users/:id/deactivate` 🔒 | nonaktifkan + **logout di semua device** |
| POST | `/admin/users/:id/revoke-sessions` 🔒 | force logout di semua device (web + mobile) |
| POST | `/admin/users/:id/unlock` 🔒 | buka lockout setelah terlalu banyak salah password |
| GET | `/admin/users/:id/sessions` 🔒 | device yang sedang login |

User tidak dihapus, hanya dinonaktifkan, supaya jejak audit tetap ada.

### D365

| Method | Path | Keterangan |
|---|---|---|
| GET | `/d365` 🔒 | connector + entity yang boleh dibaca role ini |
| GET | `/d365/:connector` 🔒 | entity + action yang diizinkan |
| GET/POST/PATCH/PUT/DELETE | `/d365/:connector/:entity` 🔒 | proxy ke D365; query OData diteruskan |
| | `/d365/:connector/:entity(<key>)` 🔒 | record tertentu |

```http
GET    /api/v1/d365/bc/customer?$filter=City eq 'Jakarta'&$select=No,Name
GET    /api/v1/d365/fo/customer(dataAreaId='usmf',CustomerAccount='US-001')
POST   /api/v1/d365/fo/so_header
PATCH  /api/v1/d365/fo/so_header(dataAreaId='usmf',SalesOrderNumber='SO-001')   # If-Match default "*"
```

Hanya bentuk `entity` dan `entity(key)` yang diterima. Path lanjutan seperti `/customer('1')/../lain` ditolak (400),
supaya request tidak bisa keluar dari entity yang sudah di-mapping. Header yang diteruskan: `Accept`, `Content-Type`,
`If-Match`, `If-None-Match`, `Prefer`, `Accept-Language`.

### Kode error penting

| HTTP | `error.code` | Arti / tindakan client |
|---|---|---|
| 400 | `validation_error` | input salah (termasuk `current_password` salah) |
| 401 | `invalid_credentials` | username/password salah |
| 401 | `invalid_token` | access token expired/invalid, jadi refresh |
| 401 | `session_ended`, `refresh_token_reused` | session dicabut, arahkan ke login |
| 403 | `user_inactive` | user dinonaktifkan |
| 403 | `forbidden` | role atau flag tidak mengizinkan |
| 409 | `conflict` | username/email/code sudah dipakai |
| 429 | `account_locked` | terlalu banyak salah password; lihat header `Retry-After` |
| 429 | `rate_limited` | terlalu banyak request dari IP ini |
| 502 | `d365_unavailable` | D365 / Entra tidak bisa dihubungi (detail ada di log server) |

---

## 9. RBAC dan proteksi user

Semua aturan ada di `rbac.toml`. Edit file itu lalu restart, tanpa ubah kode.

```toml
roles = ["superadmin", "admin", "manager", "sales", "warehouse", "viewer"]
superuser_roles = ["superadmin"]       # akses semua
admin_roles = ["superadmin", "admin"]  # akses /admin/*
protected_roles = ["admin"]            # hanya superuser yang boleh mengelola user ini
default = ["*"]                        # route/entity tanpa aturan = semua user login

[clients]                              # role mana yang boleh login dari client mana
web = ["*"]
mobile = ["sales", "manager"]

[routes]                               # path tanpa prefix /api/v1
"GET /admin/users" = ["superadmin", "admin", "manager"]
"PUT /me/username" = []                # matikan ganti username untuk semua (kecuali superuser)

[d365."*"."*"]                         # semua connector, semua entity
delete = []

[d365.bc.customer]
read = ["*"]
create = ["sales", "manager"]

[d365."*".so_lines]
all = ["sales", "manager"]             # all = action yang tidak disebut di rule ini
```

- `"*"` = semua user login; `[]` = tidak ada (kecuali superuser).
- Urutan rule entity, dari paling spesifik: `<conn>.<entity>` → `*.<entity>` → `<conn>.*` → `*.*` → `default`.
  Rule yang lebih spesifik menang: `[d365."*".so_lines] all = ["sales"]` mengizinkan sales delete so_lines walaupun `*.*` punya `delete = []`.

### Aturan proteksi user (admin API)

| Situasi | Hasil |
|---|---|
| Admin mengelola user biasa (deactivate, force logout, edit, unlock) | ✅ |
| Siapa pun mengubah akunnya sendiri lewat `/admin/...` | ❌ pakai `/me/...` |
| Admin biasa mengelola user ber-role `protected_roles` (admin lain) | ❌ hanya superuser |
| Superuser mengelola admin | ✅ |
| Siapa pun mengelola akun superuser lewat API | ❌ hanya lewat seeder/CLI |
| Admin biasa memberi role protected (misalnya membuat admin baru) | ❌ hanya superuser |
| Memberi role superuser lewat API | ❌ hanya lewat seeder/CLI |

Jadi admin/superadmin **tidak bisa di-force-logout atau dinonaktifkan oleh admin lain**, dan tidak ada jalan
privilege escalation lewat API.

### Izin ganti data per user

Setiap user punya tiga flag: `can_change_username`, `can_change_email`, `can_change_password`.
Defaultnya dari `[account]` di config, dan bisa diubah per user oleh admin (`PATCH /admin/users/:id`) atau lewat seeder.
Contoh: user sales yang username-nya sama dengan kode di D365 diberi `can_change_username = false`.

---

## 10. Referensi konfigurasi

Semua setting ada di `config.toml`. Nilai bisa diambil dari env dengan `"${VAR}"` atau `"${VAR:-default}"`.
Durasi menerima `s m h d w` (`"15m"`, `"7d"`, `"1d12h"`). Key yang salah ketik membuat server gagal start (bukan diabaikan diam-diam).

| Section | Key penting | Rekomendasi |
|---|---|---|
| `[app]` | `host`, `port`, `cors_origins`, `proxy_header`, `trusted_proxies`, timeout | `write_timeout` > timeout connector D365 terlama |
| `[jwt]` | `issuer`, `audience` | bedakan per environment, misalnya `general-auth-prod` |
| `[jwt.access]` | `secret`, `ttl` | 5-15 menit |
| `[jwt.refresh]` | `secret`, `web_ttl`, `mobile_ttl`, `max_session_age` | web 1-7 hari, mobile 30-90 hari, maksimum 90-180 hari |
| `[cookie]` | `enabled`, `name`, `path`, `domain`, `secure`, `same_site` | `secure = true`, `Strict` |
| `[database]` | `driver`, koneksi, pool, `auto_migrate` | lihat di bawah |
| `[security]` | `bcrypt_cost`, `password_min_length`, rate limit, `max_failed_logins`, `lockout_duration` | cost 12; 5 percobaan / 15 menit |
| `[account]` | `default_can_change_*` | |
| `[connectors]` | `nama = "file.toml"` | |

### Tuning pool database

| Key | Default | Panduan |
|---|---|---|
| `max_open_conns` | 25 | total koneksi semua instance harus di bawah batas DB (`max_connections` Postgres default 100). Misal 3 instance × 25 = 75. |
| `max_idle_conns` | 25 | samakan dengan `max_open_conns` supaya koneksi tidak sering dibuka-tutup |
| `conn_max_lifetime` | 30m | daur ulang koneksi; turunkan (5-10m) kalau di belakang PgBouncer/LB yang memutus koneksi lama |
| `conn_max_idle_time` | 5m | tutup koneksi idle saat trafik turun |

Setiap request terautentikasi melakukan 1 query (session + user). Login melakukan 3-4 query plus bcrypt (~250 ms di cost 12).
Bcrypt adalah beban CPU terbesar, jadi rate limit login juga melindungi CPU. SQLite memakai satu koneksi saja (khusus development).

---

## 11. Keamanan dan checklist produksi

Yang sudah ditangani di kode:

- Password: bcrypt (cost bisa diatur), maksimal 72 byte, tidak boleh sama dengan username.
- Mencegah user enumeration: waktu respons sama untuk user tidak ada vs password salah; status `inactive` hanya terlihat setelah password benar.
- Brute force: rate limit per IP + **lockout per akun** (`max_failed_logins`, `lockout_duration`), juga untuk `current_password` di `/me/*`.
- Token: HS256 dengan algoritma dikunci, `iss`/`aud`/`exp`/`nbf`/`iat` divalidasi, access dan refresh memakai **secret berbeda**.
- Session di server: revocation instan, rotasi refresh token + deteksi reuse, idle timeout + absolute timeout.
- Web: refresh token di cookie HttpOnly/Secure/SameSite, plus header wajib `X-Requested-With` (CSRF).
- Re-autentikasi (`current_password`) untuk ganti password/email/username; ganti password me-logout device lain.
- Pencegahan privilege escalation lewat `protected_roles` / `superuser_roles`.
- Header keamanan (helmet), `Cache-Control: no-store`, body limit, timeout, request ID di log.
- D365: secret dan token Entra tidak pernah keluar dari server; path entity divalidasi; `forced_query` untuk membatasi company.
- Migrasi DB berversi dalam transaksi.

Checklist sebelum produksi:

- [ ] `APP_ENV=production`. Mode ini menolak secret contoh, `cors_origins = ["*"]`, `cookie.secure = false`, dan `rbac.toml` yang tidak ada.
- [ ] Secret JWT baru (`openssl rand -hex 32`), disimpan di secret manager / env, **bukan** di file yang di-commit.
- [ ] Jalankan di belakang **HTTPS** (reverse proxy / load balancer), lalu isi `proxy_header = "X-Forwarded-For"` dan `trusted_proxies`
      dengan IP proxy supaya rate limit memakai IP asli.
- [ ] `cors_origins` berisi domain web yang tepat saja.
- [ ] `auto_migrate = false`, lalu jalankan `general-auth migrate` sebagai langkah deploy (hindari beberapa instance migrasi bersamaan).
- [ ] Koneksi DB terenkripsi (`sslmode=verify-full` / `tls=true` / `encrypt=true`).
- [ ] App Entra memakai permission minimum; user service FO bukan Admin; client secret diberi masa berlaku dan dirotasi.
- [ ] Review `rbac.toml`: role, `[clients]`, dan aturan delete.
- [ ] Ganti semua password contoh dari seeder, lalu hapus `init/users.json` di server.
- [ ] Rate limiter tersimpan di memory per instance. Kalau server di-scale horizontal, pasang juga rate limit di gateway/proxy.

---

## 12. Struktur project

```
main.go                       command: serve | migrate | seed | check | hash-password
example.config.toml           → config.toml       (setting aplikasi)
example.env                   → .env              (secret)
example.rbac.toml             → rbac.toml         (role & izin)
example.connector-bc.toml     → connector-bc.toml (Business Central)
example.connector-fo.toml     → connector-fo.toml (Finance & Operations)
init/users.example.json       → init/users.json   (seeder)
internal/config     loader TOML + ${ENV} + durasi "7d" + validasi
internal/database   koneksi multi-driver + migrasi berversi per dialect
internal/store      query users & sessions (sqlx)
internal/auth       JWT, login/refresh/session, lockout, akun sendiri, manajemen user
internal/rbac       policy role → route / entity / client
internal/d365       token Microsoft Entra + client OData D365
internal/server     Fiber routes, middleware, handler
internal/seed       seeder users.json
```

Test: `go test ./...` (integration test memakai SQLite in-memory dan fake server D365; juga memvalidasi semua file `example.*`).

### Menambah perubahan schema

Tambahkan versi baru di **akhir** `migrations` untuk **setiap** dialect di `internal/database/dialects.go`.
Jangan mengubah versi yang sudah dirilis.

### Troubleshooting

| Gejala | Penyebab umum |
|---|---|
| `invalid config ... unknown keys` | salah ketik key di `config.toml`; bandingkan dengan `example.config.toml` |
| `jwt.access.secret must be at least 32 characters` | `.env` belum diisi atau tidak ada di folder yang sama dengan `config.toml` |
| `connect postgres: ...` | DB belum jalan, database belum dibuat, atau `DB_PORT` masih port driver lain |
| `502 d365_unavailable` + log `AADSTS700016` | client ID salah / app tidak ada di tenant itu |
| log `AADSTS7000215` | client secret salah atau expired |
| D365 membalas 401 terus | app belum didaftarkan di halaman *Microsoft Entra Applications* BC/FO, atau scope salah |
| D365 membalas 403 | permission set (BC) / security role user service (FO) kurang |
| Web: refresh selalu 400 / cookie tidak terkirim | lupa `credentials: "include"`, lupa header `X-Requested-With`, atau beda site dengan `SameSite=Strict` |
| Mobile tiba-tiba `refresh_token_reused` | refresh dipanggil paralel, atau refresh token baru tidak disimpan |
