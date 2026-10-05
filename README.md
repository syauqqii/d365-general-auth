# general-auth

general-auth adalah backend kecil yang berdiri di antara aplikasi kita (web dan mobile) dan Dynamics 365, baik Business Central (BC) maupun Finance & Operations (FO).

Idenya sederhana. Aplikasi tidak boleh bicara langsung ke D365, dan tidak boleh tahu client secret Microsoft Entra. Jadi aplikasi cukup login ke sini. Setelah itu, setiap kali butuh data D365, aplikasi minta ke sini juga. Backend ini yang mengecek siapa user-nya, boleh atau tidak dia mengakses data itu, mengambil token ke Microsoft, lalu meneruskan request ke D365.

```
Web / Mobile  --(access token)-->  general-auth  --(cek session & role)
                                        |
                                        +--> token Microsoft Entra (di-cache) --> D365 BC / FO
```

Dibuat dengan Go 1.25, Fiber v2 untuk HTTP, sqlx untuk akses database (tipis, tanpa ORM berat), golang-jwt v5, dan bcrypt. Database yang didukung: PostgreSQL, MySQL/MariaDB, SQL Server, dan SQLite. Ganti database cukup lewat config, kodenya tidak perlu diubah.

## Isi

1. [Instalasi](#instalasi)
2. [Setup pertama kali](#setup-pertama-kali)
3. [Contoh isi file konfigurasi](#contoh-isi-file-konfigurasi)
4. [Mengisi user awal (seeder)](#mengisi-user-awal-seeder)
5. [Menjalankan server](#menjalankan-server)
6. [Menyiapkan akses ke D365](#menyiapkan-akses-ke-d365)
7. [Cara kerja token dan session](#cara-kerja-token-dan-session)
8. [Integrasi dari web dan mobile](#integrasi-dari-web-dan-mobile)
9. [Daftar endpoint](#daftar-endpoint)
10. [Hak akses (RBAC)](#hak-akses-rbac)
11. [Tuning database](#tuning-database)
12. [Keamanan dan persiapan produksi](#keamanan-dan-persiapan-produksi)
13. [Struktur project dan troubleshooting](#struktur-project)

## Instalasi

Yang perlu ada di komputer:

- Go 1.25 atau lebih baru (cek dengan `go version`)
- Database. Untuk coba-coba di lokal, SQLite sudah cukup dan tidak perlu install apa pun. Untuk server, pakai PostgreSQL 13+, MySQL 8 / MariaDB 10.5+, atau SQL Server 2017+.
- `openssl`, kalau mau generate secret dari terminal (opsional)

```bash
git clone <url-repo> general-auth
cd general-auth
go mod download
go build -o general-auth .        # di Windows: go build -o general-auth.exe .
```

Contoh perintah di README ini memakai `./general-auth`. Selama development, kamu juga bisa pakai `go run .` sebagai gantinya.

## Setup pertama kali

Semua file konfigurasi punya versi contohnya dengan awalan `example.`. File contoh itulah yang masuk ke git. File aslinya (`.env`, `config.toml`, `rbac.toml`, `connector-*.toml`, `init/users.json`) sengaja di-ignore, karena isinya beda di tiap environment dan sebagian berisi secret.

### 1. Copy file contoh

```bash
cp example.env               .env
cp example.config.toml       config.toml
cp example.rbac.toml         rbac.toml
cp example.connector-bc.toml connector-bc.toml
cp example.connector-fo.toml connector-fo.toml
cp init/users.example.json   init/users.json
```

Kalau kamu cuma pakai BC atau cuma FO, cukup copy connector yang dipakai. Jangan lupa hapus juga baris connector yang tidak dipakai dari bagian `[connectors]` di `config.toml`, karena server akan menolak start kalau ada file connector yang hilang.

### 2. Isi secret di `.env`

Buat dua secret acak untuk JWT. Keduanya harus berbeda dan panjangnya minimal 32 karakter:

```bash
openssl rand -hex 32    # hasilnya untuk JWT_ACCESS_SECRET
openssl rand -hex 32    # hasilnya untuk JWT_REFRESH_SECRET
```

Setelah itu isi koneksi database (`DB_*`) dan kredensial D365 (`BC_*` / `FO_*`). Cara mendapatkan kredensial D365 dijelaskan di bagian [Menyiapkan akses ke D365](#menyiapkan-akses-ke-d365).

`config.toml` tidak menyimpan secret apa pun. Isinya hanya referensi seperti `${JWT_ACCESS_SECRET}` yang diambil dari `.env`. Di server, kamu bisa langsung set environment variable tanpa file `.env`; environment variable selalu menang atas isi `.env`.

### 3. Pilih database

Atur lewat `.env`:

| Database | `DB_DRIVER` | `DB_PORT` | Catatan |
|---|---|---|---|
| PostgreSQL | `postgres` | 5432 | `DB_SSLMODE`: `disable` untuk lokal, `require` atau `verify-full` untuk produksi |
| MySQL / MariaDB | `mysql` | 3306 | `DB_TLS`: `false`, `true`, atau `skip-verify` |
| SQL Server | `sqlserver` | 1433 | `DB_ENCRYPT=true`, `DB_TRUST_SERVER_CERT=false` |
| SQLite | `sqlite` | tidak dipakai | `DB_NAME` diisi path file, misalnya `data/general-auth.db`. Folder `data/` harus sudah ada. |

Database-nya sendiri harus dibuat dulu (`CREATE DATABASE general_auth;`). Tabel-tabelnya akan dibuat otomatis oleh migrasi.

Waktu pindah driver, ingat ganti `DB_PORT` juga. Ini kesalahan yang paling sering terjadi.

### 4. Sesuaikan `config.toml`

Biasanya yang perlu diubah cuma beberapa:

- `cors_origins`: isi dengan alamat web app kamu, misalnya `["https://portal.perusahaan.com"]`. Aplikasi mobile tidak butuh CORS.
- Umur token di `[jwt.access]` dan `[jwt.refresh]`. Default-nya sudah masuk akal untuk kebanyakan kasus.
- `[security]`, kalau mau mengubah batas percobaan login.
- `[account]`, untuk menentukan apakah user baru boleh mengganti username, email, atau password-nya sendiri.

### 5. Atur hak akses di `rbac.toml`

File contohnya sudah bisa dipakai langsung. Penjelasan lengkapnya ada di bagian [Hak akses (RBAC)](#hak-akses-rbac).

### 6. Cek hasilnya

```bash
./general-auth check
```

Kalau semua beres, outputnya kira-kira `config OK: env=development db=postgres connectors=2`. Kalau ada yang salah, misalnya key salah ketik atau secret terlalu pendek, pesan error-nya akan menunjukkan bagian mana yang harus dibetulkan.

## Contoh isi file konfigurasi

Berikut isi lengkap file-file contoh di repo ini, persis seperti aslinya. Komentar di dalam file sengaja ditulis dalam bahasa Inggris supaya konsisten dengan kode.

### `.env` (dari `example.env`)

```dotenv
# Copy to .env (next to config.toml). .env is git-ignored.
# Real environment variables override values in this file.

# --- App -------------------------------------------------------------------
APP_ENV=development
APP_HOST=0.0.0.0
APP_PORT=3000
APP_PROXY_HEADER=

# --- JWT -------------------------------------------------------------------
# Two DIFFERENT random secrets, at least 32 characters. Generate each with:
#   openssl rand -hex 32
JWT_ACCESS_SECRET=change-me-access-secret-at-least-32-characters
JWT_REFRESH_SECRET=change-me-refresh-secret-at-least-32-characters
JWT_ACCESS_TTL=15m
JWT_REFRESH_WEB_TTL=7d
JWT_REFRESH_MOBILE_TTL=90d
JWT_MAX_SESSION_AGE=180d

# --- Database --------------------------------------------------------------
# postgres | mysql | sqlite | sqlserver
DB_DRIVER=postgres
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=postgres
DB_NAME=general_auth
DB_SSLMODE=disable
DB_TLS=false
DB_ENCRYPT=true
DB_TRUST_SERVER_CERT=false
DB_DSN=

# --- D365 Business Central (connector-bc.toml) -----------------------------
BC_TENANT_ID=00000000-0000-0000-0000-000000000000
BC_CLIENT_ID=00000000-0000-0000-0000-000000000000
BC_CLIENT_SECRET=your-client-secret
BC_ENVIRONMENT=Production
BC_COMPANY=CRONUS%20International%20Ltd.

# --- D365 Finance & Operations (connector-fo.toml) -------------------------
FO_TENANT_ID=00000000-0000-0000-0000-000000000000
FO_CLIENT_ID=00000000-0000-0000-0000-000000000000
FO_CLIENT_SECRET=your-client-secret
FO_ENV_HOST=your-env.operations.dynamics.com
```

### `config.toml` (dari `example.config.toml`)

```toml
# general-auth configuration.
# Copy to config.toml and adjust. config.toml is git-ignored.
#
# Any value may reference an environment variable: "${NAME}" or
# "${NAME:-default}". A .env file next to this file is loaded first, so keep
# secrets in .env (see example.env) and never write them here.
#
# Durations accept s, m, h, d (day) and w (week): "15m", "12h", "7d", "1d12h".

[app]
name = "general-auth"
env = "${APP_ENV:-development}"     # "production" turns on strict checks (see README)
host = "${APP_HOST:-0.0.0.0}"
port = "${APP_PORT:-3000}"
body_limit_mb = 10
read_timeout = "30s"
write_timeout = "90s"                # must exceed the slowest D365 call
idle_timeout = "120s"
# Exact browser origins allowed to call the API (scheme + host + port).
# Mobile apps do not need CORS. Use [] to disable CORS entirely.
cors_origins = ["http://localhost:5173"]
# Behind a reverse proxy / load balancer: header with the real client IP and
# the proxy addresses allowed to set it. Leave empty when exposed directly.
proxy_header = "${APP_PROXY_HEADER:-}"
trusted_proxies = []

[jwt]
issuer = "general-auth"
audience = "general-auth-api"

# Access token: short-lived, sent as "Authorization: Bearer ..." on every call.
[jwt.access]
secret = "${JWT_ACCESS_SECRET}"
ttl = "${JWT_ACCESS_TTL:-15m}"

# Refresh token: used only on /auth/refresh, rotated on every use.
[jwt.refresh]
secret = "${JWT_REFRESH_SECRET}"
web_ttl = "${JWT_REFRESH_WEB_TTL:-7d}"        # idle timeout for browsers
mobile_ttl = "${JWT_REFRESH_MOBILE_TTL:-90d}" # idle timeout for mobile apps
max_session_age = "${JWT_MAX_SESSION_AGE:-180d}" # absolute limit, "0" = off

# Web clients receive the refresh token in this HttpOnly cookie instead of the
# response body. Mobile clients always receive it in the body.
[cookie]
enabled = true
name = "ga_refresh"
path = "/api/v1/auth"
domain = ""                          # "" = the API host only
secure = true                        # HTTPS only (browsers allow http://localhost)
same_site = "Strict"                 # Strict | Lax | None (None needs secure)

[database]
driver = "${DB_DRIVER:-postgres}"    # postgres | mysql | sqlite | sqlserver
host = "${DB_HOST:-localhost}"
port = "${DB_PORT:-5432}"
user = "${DB_USER:-postgres}"
password = "${DB_PASSWORD}"
name = "${DB_NAME:-general_auth}"    # for sqlite: the file path, e.g. "data/general-auth.db"
dsn = "${DB_DSN:-}"                  # optional raw DSN, overrides the fields above
max_open_conns = 25                  # keep below the database's own connection limit
max_idle_conns = 25                  # = max_open_conns avoids reconnect churn
conn_max_lifetime = "30m"            # recycle connections (load balancers, failover)
conn_max_idle_time = "5m"
auto_migrate = true                  # set false in production and run `general-auth migrate`

# Extra DSN parameters per driver; only the table of the active driver is
# used. Empty values are skipped (driver default applies).
[database.params.postgres]
sslmode = "${DB_SSLMODE:-prefer}"          # disable | prefer | require | verify-full

[database.params.mysql]
tls = "${DB_TLS:-false}"                   # false | true | skip-verify | preferred
charset = "utf8mb4"

[database.params.sqlserver]
encrypt = "${DB_ENCRYPT:-true}"            # true | false | disable
TrustServerCertificate = "${DB_TRUST_SERVER_CERT:-false}"

[database.params.sqlite]

[security]
bcrypt_cost = 12                     # 12 is about 250 ms per hash; raise as hardware gets faster
password_min_length = 8
login_rate_limit = 10                # requests per IP per window on login/refresh/me-changes
login_rate_window = "1m"
max_failed_logins = 5                # wrong passwords before the account locks, 0 = off
lockout_duration = "15m"
session_cleanup_interval = "1h"      # delete expired/revoked sessions

# Defaults for new users; each user can be changed by an admin later.
[account]
default_can_change_username = false
default_can_change_email = true
default_can_change_password = true

[rbac]
file = "rbac.toml"

# D365 connectors: name = file. The name is used in the URL:
#   /api/v1/d365/<name>/<entity>
[connectors]
bc = "connector-bc.toml"
fo = "connector-fo.toml"

[seed]
users_file = "init/users.json"
```

### `rbac.toml` (dari `example.rbac.toml`)

```toml
# Role based access control. Copy to rbac.toml, edit, restart the server.
#
# "*" in a role list means any logged-in user. An empty list [] means nobody
# (except superuser_roles). A rule that is not written falls back to the
# next, less specific one, and finally to `default`.

# Roles a user may be given; the admin API and the seeder reject others.
roles = ["superadmin", "admin", "manager", "sales", "warehouse", "viewer"]

# Full access to every route and entity. Accounts with these roles can only
# be created or changed from the server CLI (seeder), never through the API.
superuser_roles = ["superadmin"]

# Who may use /admin/* (user management) unless [routes] says otherwise.
admin_roles = ["superadmin", "admin"]

# Users with these roles cannot be edited, deactivated or force-logged-out by
# a normal admin; only a superuser can. Only a superuser may grant them.
protected_roles = ["admin"]

# Default for every route / D365 entity without its own rule.
# ["*"] = all-access mode for every logged-in user.
default = ["*"]

# Which roles may log in from which client type ("client" in the login body).
[clients]
web = ["*"]
mobile = ["*"]

# API routes: "METHOD /path" with the path as registered, WITHOUT the /api/v1
# prefix. "*" as METHOD matches every method. Unlisted routes use `default`
# (or admin_roles for /admin/*).
[routes]
"GET /admin/users" = ["superadmin", "admin", "manager"]
"GET /admin/users/:id" = ["superadmin", "admin", "manager"]

# D365 entities: [d365.<connector>.<entity>], <entity> being a key of
# [endpoints] in the connector file. "*" matches any connector or entity.
# Actions: read (GET), create (POST), update (PATCH/PUT), delete (DELETE);
# `all` covers the actions not listed in the same rule.
# Lookup order: <conn>.<entity> -> *.<entity> -> <conn>.* -> *.* -> default

# Deleting in D365 is reserved for superusers unless a more specific rule
# allows it.
[d365."*"."*"]
delete = []

[d365."*".customer]
read = ["*"]
create = ["manager", "sales"]
update = ["manager", "sales"]

[d365."*".item]
read = ["*"]
create = ["manager", "warehouse"]
update = ["manager", "warehouse"]

[d365."*".so_header]
read = ["manager", "sales"]
create = ["manager", "sales"]
update = ["manager", "sales"]

[d365."*".so_lines]
all = ["manager", "sales"]
```

### `connector-bc.toml` (dari `example.connector-bc.toml`)

```toml
# Dynamics 365 Business Central connector.
# Copy to connector-bc.toml (git-ignored) and fill in your tenant.
#
# Setup (https://learn.microsoft.com/dynamics365/business-central/dev-itpro/administration/automation-apis-using-s2s-authentication):
#   1. Microsoft Entra admin center > App registrations > New registration.
#   2. Certificates & secrets > New client secret (copy the value).
#   3. API permissions > Dynamics 365 Business Central > Application
#      permissions > API.ReadWrite.All > Grant admin consent.
#   4. In Business Central open "Microsoft Entra Applications" > New, paste
#      the client ID, set State = Enabled and assign least-privilege
#      permission sets (SUPER cannot be assigned to apps).

d365_product = "bc"                               # "bc" or "fo"

# Microsoft Entra ID, client credentials flow (OAuth 2.0 v2 endpoint).
d365_tenant_id = "${BC_TENANT_ID}"                # Directory (tenant) ID
d365_client_id = "${BC_CLIENT_ID}"                # Application (client) ID
d365_client_secret = "${BC_CLIENT_SECRET}"
d365_grant_type = "client_credentials"
d365_scope = "https://api.businesscentral.dynamics.com/.default"
d365_auth_url = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"  # %s = tenant ID

# Base URL; %s is replaced with the endpoint name from [endpoints] (plus the
# record key, if any). Pick ONE style:
#
#   OData web services (pages published in "Web Services"):
#     https://api.businesscentral.dynamics.com/v2.0/<tenant>/<environment>/ODataV4/Company('<company name>')/%s
#   Standard API v2.0 (entity sets: customers, items, salesOrders, ...):
#     https://api.businesscentral.dynamics.com/v2.0/<tenant>/<environment>/api/v2.0/companies(<company id>)/%s
#
# Spaces in the company name must be written as %20.
api_url = "https://api.businesscentral.dynamics.com/v2.0/${BC_TENANT_ID}/${BC_ENVIRONMENT:-Production}/ODataV4/Company('${BC_COMPANY}')/%s"

timeout = "30s"

# Generic entity name used in /api/v1/d365/bc/<entity> = BC web service name.
# Add entities here; no code changes needed. Permissions live in rbac.toml.
[endpoints]
customer = "Customers_Card"
item = "Item_Card_Excel"
so_header = "SOHeader"
so_lines = "SOLines"

# Added to collection reads (GET without a key) when the client did not
# send the parameter.
[default_query]
"$top" = "200"

# Always applied, replacing whatever the client sent.
[forced_query]
```

### `connector-fo.toml` (dari `example.connector-fo.toml`)

```toml
# Dynamics 365 Finance & Operations (Finance, Supply Chain Management)
# connector. Copy to connector-fo.toml (git-ignored) and fill in your
# environment.
#
# Setup (https://learn.microsoft.com/dynamics365/fin-ops-core/dev-itpro/data-entities/services-home-page):
#   1. Microsoft Entra admin center > App registrations > New registration.
#   2. Certificates & secrets > New client secret (copy the value).
#   3. In F&O: System administration > Setup > Microsoft Entra applications
#      > New: client ID + a dedicated service user with only the security
#      roles this integration needs (not Admin). F&O applies that user's
#      permissions and default company to every call.

d365_product = "fo"                               # "bc" or "fo"

# Microsoft Entra ID, client credentials flow (OAuth 2.0 v2 endpoint).
# The scope is your F&O environment URL (no trailing slash) + "/.default".
d365_tenant_id = "${FO_TENANT_ID}"
d365_client_id = "${FO_CLIENT_ID}"
d365_client_secret = "${FO_CLIENT_SECRET}"
d365_grant_type = "client_credentials"
d365_scope = "https://${FO_ENV_HOST}/.default"
d365_auth_url = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"  # %s = tenant ID

# OData root is <environment URL>/data; %s is replaced with the entity set
# name (plus the record key, if any), e.g.
#   https://contoso.operations.dynamics.com/data/CustomersV3(dataAreaId='usmf',CustomerAccount='US-001')
# Browse <environment URL>/data/$metadata for every public entity set.
api_url = "https://${FO_ENV_HOST}/data/%s"

timeout = "60s"

# Generic entity name used in /api/v1/d365/fo/<entity> = F&O public entity
# set name. Keys in F&O need every key field, usually including dataAreaId.
[endpoints]
customer = "CustomersV3"
item = "ReleasedProductsV2"
so_header = "SalesOrderHeadersV2"
so_lines = "SalesOrderLines"

# Added to collection reads (GET without a key) when the client did not
# send the parameter.
[default_query]
"$top" = "200"

# Always applied, replacing whatever the client sent. F&O returns only the
# service user's default company; forcing cross-company=false stops clients
# from reading other legal entities with ?cross-company=true.
[forced_query]
cross-company = "false"
```

### `init/users.json` (dari `init/users.example.json`)

```json
[
  {
    "username": "superadmin",
    "email": "superadmin@example.com",
    "name": "Super Administrator",
    "password": "ChangeMe-Super-123",
    "role": "superadmin",
    "can_change_username": false,
    "can_change_email": false,
    "can_change_password": true
  },
  {
    "username": "admin",
    "email": "admin@example.com",
    "code": "ADM001",
    "name": "Administrator",
    "password": "ChangeMe-Admin-123",
    "role": "admin"
  },
  {
    "username": "sales01",
    "email": "sales01@example.com",
    "code": "S001",
    "name": "Sales One",
    "password": "ChangeMe-Sales-123",
    "role": "sales",
    "can_change_username": false,
    "can_change_email": false,
    "can_change_password": true
  },
  {
    "username": "viewer01",
    "name": "Viewer (password_hash of ViewerPass123)",
    "password_hash": "$2a$12$z9TJDazustwHQZZFT5vM2.jNJrwqopBg7m3K1.PIMcHOpDWLEe25S",
    "role": "viewer",
    "is_active": false
  }
]
```

## Mengisi user awal (seeder)

Seeder membuat user dari file `init/users.json`. Kalau file itu belum ada, seeder akan berhenti dan menyuruh kamu membuatnya dulu:

```
seed file .../init/users.json not found.
Create it first (copy init/users.example.json to .../init/users.json and edit the users), then run the seeder again.
```

Jadi alurnya:

```bash
cp init/users.example.json init/users.json    # lalu edit isinya
./general-auth seed
```

Seeder menjalankan migrasi terlebih dahulu, lalu membuat user yang belum ada. User yang sudah ada (dicocokkan dari username) dilewati. Kalau kamu ingin menimpa data user yang sudah ada dengan isi file, pakai `./general-auth seed -update`. Perlu diingat, kalau entri tersebut berisi `password`, user itu otomatis ter-logout dari semua device.

Penjelasan field di `users.json`:

| Field | Wajib? | Keterangan |
|---|---|---|
| `username` | ya | 3 sampai 100 karakter: huruf kecil, angka, titik, garis bawah, atau strip. Huruf besar otomatis dijadikan kecil. |
| `email` | tidak | harus unik |
| `code` | tidak | kode lain untuk login, misalnya sales id atau NIK. Harus unik. |
| `password` atau `password_hash` | pilih salah satu | `password` diisi teks biasa dan akan di-hash saat seeding. `password_hash` diisi hash bcrypt hasil `./general-auth hash-password 'passwordnya'`. |
| `role` | ya | harus terdaftar di `roles` pada `rbac.toml` |
| `is_active` | tidak | default `true` |
| `can_change_username`, `can_change_email`, `can_change_password` | tidak | kalau tidak diisi, ikut default di `[account]` pada config |

Beberapa hal yang perlu diketahui:

- Akun superadmin hanya bisa dibuat dan diubah lewat seeder. Lewat API tidak bisa, dan memang sengaja begitu.
- Untuk server produksi, lebih aman memakai `password_hash` supaya file tidak menyimpan password asli. Hapus `init/users.json` dari server setelah seeding selesai.

## Menjalankan server

```bash
./general-auth serve        # atau cukup ./general-auth
curl localhost:3000/health
```

Perintah yang tersedia:

| Perintah | Fungsi |
|---|---|
| `serve` | menjalankan API. Ini perintah default kalau tidak menulis apa-apa. |
| `migrate` | menjalankan migrasi database |
| `seed` / `seed -update` | mengisi user dari `init/users.json` |
| `check` | memeriksa semua file konfigurasi lalu keluar |
| `hash-password <password>` | membuat hash bcrypt untuk `password_hash` |

Semua perintah bisa diberi `-config path/ke/config.toml`. Kalau tidak diberi, yang dipakai adalah `config.toml` di folder saat ini (atau isi env `CONFIG_PATH`). Server berhenti dengan rapi kalau menerima Ctrl+C atau `SIGTERM`.

## Menyiapkan akses ke D365

BC dan FO sama-sama memakai OAuth 2.0 client credentials ke Microsoft Entra ID. Artinya backend login sebagai aplikasi, bukan sebagai user. Token diminta ke:

```
POST https://login.microsoftonline.com/<tenant-id>/oauth2/v2.0/token
```

Perbedaan BC dan FO ada di scope dan URL API-nya:

| | Business Central | Finance & Operations |
|---|---|---|
| Scope | `https://api.businesscentral.dynamics.com/.default` | `https://<env>.operations.dynamics.com/.default` |
| URL OData | `https://api.businesscentral.dynamics.com/v2.0/<tenant>/<environment>/ODataV4/Company('<company>')/<entity>` | `https://<env>.operations.dynamics.com/data/<entity>` |
| Contoh nama entity | `Customers_Card` (web service), atau `customers` kalau pakai API v2.0 | `CustomersV3`, `ReleasedProductsV2`, `SalesOrderHeadersV2`, `SalesOrderLines` |
| Contoh ambil 1 record | `Customers_Card('10000')` | `CustomersV3(dataAreaId='usmf',CustomerAccount='US-001')` |

### Business Central

1. Buka Microsoft Entra admin center, masuk ke App registrations, lalu buat registrasi baru.
2. Di menu Certificates & secrets, buat client secret baru, lalu salin nilainya ke `BC_CLIENT_SECRET`.
3. Di menu API permissions, tambahkan Dynamics 365 Business Central dengan tipe Application permissions, pilih `API.ReadWrite.All`, lalu klik Grant admin consent.
4. Di Business Central, buka halaman Microsoft Entra Applications dan buat entri baru. Masukkan client ID, ubah State menjadi Enabled, lalu berikan permission set seperlunya. Permission set SUPER memang tidak bisa diberikan ke aplikasi.
5. Kalau kamu memakai OData web services, publish page yang dibutuhkan di halaman Web Services. Nama service yang muncul di sana adalah nama yang ditulis di `[endpoints]`.

Referensi resmi: [Using Service to Service Authentication](https://learn.microsoft.com/dynamics365/business-central/dev-itpro/administration/automation-apis-using-s2s-authentication)

### Finance & Operations

1. Di Microsoft Entra admin center, buat app registration dan client secret seperti di atas.
2. Di FO, buka System administration > Setup > Microsoft Entra applications, lalu buat entri baru dengan client ID tadi. Di kolom User ID, pilih user khusus untuk integrasi ini yang hanya punya security role seperlunya. Jangan pakai Admin. Semua request dari backend akan berjalan dengan hak akses user ini.
3. Isi `FO_ENV_HOST` dengan host environment-nya saja, tanpa `https://` dan tanpa garis miring di belakang. Contoh: `contoso.operations.dynamics.com`.
4. Daftar nama entity yang tersedia bisa dilihat di `https://<env>.operations.dynamics.com/data/$metadata`.

FO punya perilaku yang perlu diperhatikan: secara default ia hanya mengembalikan data dari company default milik user integrasi tadi. Kalau request diberi `?cross-company=true`, data dari semua company yang bisa diakses user itu ikut keluar. Karena itu connector FO di contoh memaksa `cross-company=false` lewat `[forced_query]`, supaya aplikasi tidak bisa mengintip data legal entity lain.

Referensi resmi: [Service endpoints overview](https://learn.microsoft.com/dynamics365/fin-ops-core/dev-itpro/data-entities/services-home-page) dan [OData di FO](https://learn.microsoft.com/dynamics365/fin-ops-core/dev-itpro/data-entities/odata)

### Tentang file connector

Bagian `[endpoints]` memetakan nama yang dipakai aplikasi ke nama entity di D365. Misalnya `customer = "CustomersV3"` berarti aplikasi cukup memanggil `/api/v1/d365/fo/customer`. Menambah entity baru cukup dengan menambah satu baris lalu restart server, tanpa mengubah kode.

`[default_query]` berisi parameter yang ditambahkan otomatis saat aplikasi mengambil daftar data (GET tanpa key) dan tidak mengirim parameter itu sendiri. Contohnya `$top = "200"`, supaya tidak ada yang tanpa sengaja menarik ribuan baris sekaligus.

`[forced_query]` berisi parameter yang selalu dipakai dan menimpa apa pun yang dikirim aplikasi. Contohnya `cross-company = "false"` di FO.

Untuk menambah environment baru, misalnya BC produksi terpisah dari BC development, buat file connector baru lalu daftarkan di `[connectors]`:

```toml
[connectors]
bc = "connector-bc.toml"
bc_prod = "connector-bc-prod.toml"
```

Token dari Microsoft disimpan di memory dan dipakai ulang sampai kira-kira satu menit sebelum kedaluwarsa. Kalau D365 tiba-tiba menolak dengan 401, backend akan meminta token baru lalu mencoba sekali lagi.

## Cara kerja token dan session

### Web dan mobile memakai API yang sama

Tidak ada endpoint khusus web atau khusus mobile. Semuanya di `/api/v1/...` dan memakai jenis token yang sama: access token dan refresh token.

Alasannya, kalau endpoint dipisah, logika dan aturan aksesnya harus dipelihara dua kali, dan cepat atau lambat keduanya akan berbeda tanpa disadari. Padahal perbedaan web dan mobile sebenarnya cuma dua: di mana refresh token disimpan, dan berapa lama umurnya.

Saat login, aplikasi menyebutkan jenisnya lewat `"client": "web"` atau `"client": "mobile"` di body, atau lewat header `X-Client-Type`. Kalau tidak disebutkan, dianggap web.

| | Web (browser) | Mobile |
|---|---|---|
| Access token | dikirim di body JSON. Simpan di memory saja, jangan di localStorage. | dikirim di body JSON, simpan di memory |
| Refresh token | dikirim sebagai cookie HttpOnly, sehingga JavaScript tidak bisa membacanya | dikirim di body JSON. Simpan di Keychain (iOS) atau Keystore (Android). |
| Berapa lama boleh tidak dipakai | `web_ttl`, default 7 hari | `mobile_ttl`, default 90 hari |
| Batas maksimal sejak login | `max_session_age`, default 180 hari | sama |

Mobile tetap memakai refresh token karena access token sengaja dibuat pendek (15 menit). Kalau access token bocor, misalnya lewat log atau crash report, token itu cepat tidak berguna lagi. Refresh token yang umurnya panjang hanya dikirim ke satu endpoint dan disimpan di tempat aman milik sistem operasi. Dari sisi user, rasanya sama seperti "login sekali, awet": selama aplikasi dibuka minimal sekali dalam 90 hari, user tidak perlu login lagi.

### Session

Setiap login tercatat sebagai satu session, satu per device. Semua token membawa ID session-nya, dan setiap request yang butuh login akan mengecek session itu di database.

Dengan cara ini, logout, menonaktifkan user, ganti password, atau force logout oleh admin langsung berlaku saat itu juga di web maupun mobile. Tidak perlu menunggu token kedaluwarsa. Perubahan role juga langsung berlaku, karena role selalu dibaca dari database, bukan dari isi token.

Refresh token juga dirotasi: setiap kali dipakai, keluar refresh token baru dan yang lama tidak berlaku lagi. Kalau ada yang mencoba memakai refresh token lama, besar kemungkinan token itu sudah dicuri. Dalam kondisi itu seluruh session langsung dicabut dan user harus login ulang.

## Integrasi dari web dan mobile

### Web

```js
// login
const res = await fetch(`${API}/api/v1/auth/login`, {
  method: "POST",
  credentials: "include",   // supaya cookie refresh tersimpan dan ikut terkirim
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ identifier, password, client: "web" }),
});
const { data } = await res.json();
let accessToken = data.access_token;   // simpan di memory saja

// memanggil API
fetch(`${API}/api/v1/me`, {
  headers: { Authorization: `Bearer ${accessToken}` },
});

// minta access token baru, saat dapat 401 atau menjelang expires_at
const r = await fetch(`${API}/api/v1/auth/refresh`, {
  method: "POST",
  credentials: "include",
  headers: { "X-Requested-With": "fetch" },   // wajib kalau refresh lewat cookie
});
```

Beberapa catatan untuk web:

- Setelah halaman di-reload, access token di memory hilang. Panggil `/auth/refresh` dulu untuk mendapatkan yang baru dari cookie.
- Header `X-Requested-With` wajib dikirim saat refresh atau logout lewat cookie. Header ini yang mencegah situs lain memicu refresh atas nama user (CSRF).
- Kalau alamat web dan API berbeda, masukkan alamat web ke `cors_origins`. Isinya tidak boleh `"*"`.
- Cookie memakai `SameSite=Strict`, jadi web dan API sebaiknya berada di domain induk yang sama, misalnya `app.perusahaan.com` dan `api.perusahaan.com`. Kalau domainnya benar-benar berbeda, ubah `same_site` menjadi `"None"` (dan `secure` harus tetap `true`).

### Mobile

```http
POST /api/v1/auth/login
Content-Type: application/json

{ "identifier": "S001", "password": "...", "client": "mobile" }
```

Responsnya berisi `access_token`, `expires_at`, `refresh_token`, `refresh_expires_at`, dan data `user`.

Beberapa catatan untuk mobile:

- Simpan `refresh_token` di Keychain atau Keystore, dan `access_token` di memory.
- Kalau dapat 401 dengan kode `invalid_token`, atau waktunya sudah mendekati `expires_at`, panggil `POST /api/v1/auth/refresh` dengan body `{ "refresh_token": "..." }`. Selalu simpan refresh token baru dari responsnya, karena yang lama langsung tidak berlaku.
- Pastikan hanya ada satu proses refresh yang berjalan pada satu waktu. Kalau dua refresh berjalan bersamaan dengan token yang sama, yang kedua dianggap pemakaian ulang dan session-nya dicabut.
- Kalau dapat `session_ended`, `refresh_token_reused`, atau `user_inactive`, arahkan user ke halaman login.
- Untuk logout, panggil `POST /api/v1/auth/logout` dengan body `{ "refresh_token": "..." }`. Ini tetap berhasil walaupun access token-nya sudah kedaluwarsa.

## Daftar endpoint

Semua endpoint ada di bawah `/api/v1`. Format respons:

```json
{ "success": true, "data": { } }
{ "success": false, "error": { "code": "invalid_credentials", "message": "..." } }
```

Satu-satunya pengecualian adalah proxy D365, yang meneruskan respons dari D365 apa adanya.

Kolom "Login" menunjukkan apakah endpoint itu butuh header `Authorization: Bearer <access_token>`.

### Login dan logout

| Method | Path | Login | Keterangan |
|---|---|---|---|
| POST | `/auth/login` | tidak | body: `identifier`, `password`, `client` (opsional), `login_type` (opsional). `identifier` bisa berupa username, email, atau code. `login_type` bisa `auto` (default), `username`, `email`, atau `code`. |
| POST | `/auth/refresh` | tidak | mobile mengirim `{ "refresh_token": "..." }`. Web cukup mengirim cookie plus header `X-Requested-With`. |
| POST | `/auth/logout` | tidak | menerima access token, refresh token, atau keduanya. Session yang bersangkutan akan dicabut. |
| POST | `/auth/logout-all` | ya | logout dari semua device |

### Akun sendiri

Setiap perubahan wajib menyertakan `current_password`, dan user harus punya izinnya (`can_change_username`, `can_change_email`, atau `can_change_password`). Password yang salah di sini dihitung sebagai percobaan login gagal.

| Method | Path | Body |
|---|---|---|
| GET | `/me` | - |
| PUT | `/me/password` | `current_password`, `new_password`. Device lain otomatis ter-logout. |
| PUT | `/me/email` | `current_password`, `email` |
| PUT | `/me/username` | `current_password`, `username` |
| GET | `/me/sessions` | - (daftar device yang sedang login) |
| DELETE | `/me/sessions/:id` | - (logout satu device tertentu) |

Semua endpoint di tabel ini butuh login.

### Manajemen user (admin)

Secara default hanya bisa diakses role di `admin_roles`. Semuanya butuh login.

| Method | Path | Keterangan |
|---|---|---|
| GET | `/admin/users` | daftar user. Bisa difilter dengan `?q=`, `?role=`, `?active=true`, `?limit=`, `?offset=` |
| POST | `/admin/users` | buat user baru. Isi body sama seperti satu entri di `users.json`. |
| GET | `/admin/users/:id` | detail user |
| PATCH | `/admin/users/:id` | ubah sebagian data: `name`, `email`, `code`, `username`, `role`, `password`, `is_active`, `can_change_username`, `can_change_email`, `can_change_password` |
| POST | `/admin/users/:id/activate` | aktifkan user |
| POST | `/admin/users/:id/deactivate` | nonaktifkan user, sekaligus logout dari semua device |
| POST | `/admin/users/:id/revoke-sessions` | paksa logout dari semua device, web maupun mobile |
| POST | `/admin/users/:id/unlock` | buka kunci akun yang terkunci karena salah password berkali-kali |
| GET | `/admin/users/:id/sessions` | daftar device yang sedang login |

User tidak pernah dihapus, hanya dinonaktifkan, supaya riwayatnya tetap ada.

### D365

Semua butuh login.

| Method | Path | Keterangan |
|---|---|---|
| GET | `/d365` | daftar connector dan entity yang boleh dibaca user ini |
| GET | `/d365/:connector` | daftar entity beserta aksi yang diizinkan |
| GET, POST, PATCH, PUT, DELETE | `/d365/:connector/:entity` | diteruskan ke D365, termasuk query OData-nya |
| sama | `/d365/:connector/:entity(<key>)` | untuk satu record tertentu |

Contoh:

```http
GET    /api/v1/d365/bc/customer?$filter=City eq 'Jakarta'&$select=No,Name
GET    /api/v1/d365/fo/customer(dataAreaId='usmf',CustomerAccount='US-001')
POST   /api/v1/d365/fo/so_header
PATCH  /api/v1/d365/fo/so_header(dataAreaId='usmf',SalesOrderNumber='SO-001')
```

Untuk PATCH, PUT, dan DELETE, header `If-Match: *` ditambahkan otomatis kalau aplikasi tidak mengirimnya.

Hanya bentuk `entity` dan `entity(key)` yang diterima. Path yang lebih panjang, misalnya `/customer('1')/../yang-lain`, langsung ditolak, supaya request tidak bisa menjangkau entity yang tidak terdaftar di connector.

Header dari aplikasi yang ikut diteruskan ke D365: `Accept`, `Content-Type`, `If-Match`, `If-None-Match`, `Prefer`, dan `Accept-Language`.

### Kode error yang perlu ditangani aplikasi

| HTTP | `error.code` | Artinya |
|---|---|---|
| 400 | `validation_error` | input tidak valid, termasuk `current_password` yang salah |
| 401 | `invalid_credentials` | username atau password salah |
| 401 | `invalid_token` | access token kedaluwarsa atau tidak valid. Lakukan refresh. |
| 401 | `session_ended` / `refresh_token_reused` | session sudah dicabut. Arahkan ke halaman login. |
| 403 | `user_inactive` | akun dinonaktifkan |
| 403 | `forbidden` | role atau izin user tidak mencukupi |
| 409 | `conflict` | username, email, atau code sudah dipakai orang lain |
| 429 | `account_locked` | terlalu banyak salah password. Header `Retry-After` berisi berapa detik harus menunggu. |
| 429 | `rate_limited` | terlalu banyak request dari IP yang sama |
| 502 | `d365_unavailable` | D365 atau Microsoft Entra tidak bisa dihubungi. Detailnya ada di log server. |

## Hak akses (RBAC)

Semua aturan akses ada di `rbac.toml`. Setelah mengubahnya, cukup restart server.

Cara membaca daftar role:

- `"*"` berarti semua user yang sudah login.
- `[]` (kosong) berarti tidak ada yang boleh, kecuali superuser.

Ada beberapa kelompok role khusus:

- `superuser_roles` boleh mengakses semuanya.
- `admin_roles` boleh membuka `/admin/*`, kecuali diatur lain di `[routes]`.
- `protected_roles` adalah role yang user-nya hanya boleh dikelola oleh superuser.
- `default` berlaku untuk route atau entity yang tidak punya aturan sendiri. Contoh bawaannya `["*"]`, jadi semuanya terbuka untuk user yang sudah login, kecuali yang dibatasi secara eksplisit.

`[clients]` mengatur role mana yang boleh login dari web dan mana yang boleh dari mobile. Misalnya `mobile = ["sales", "manager"]` berarti hanya sales dan manager yang bisa login dari aplikasi mobile.

`[routes]` mengatur akses per endpoint. Path-nya ditulis tanpa `/api/v1`. Contoh: `"PUT /me/username" = []` mematikan fitur ganti username untuk semua orang.

`[d365.<connector>.<entity>]` mengatur akses ke data D365 per aksi: `read` (GET), `create` (POST), `update` (PATCH/PUT), dan `delete` (DELETE). Ada juga `all` untuk aksi yang tidak disebut di aturan itu. Connector dan entity boleh diisi `"*"`.

Kalau ada beberapa aturan yang cocok, yang paling spesifik yang dipakai. Urutannya dari yang paling spesifik:

1. `<connector>.<entity>`
2. `*.<entity>`
3. `<connector>.*`
4. `*.*`
5. `default`

Akibatnya, kalau `*.*` berisi `delete = []` tapi `[d365."*".so_lines]` berisi `all = ["sales"]`, sales tetap boleh menghapus `so_lines`, karena aturan yang kedua lebih spesifik.

### Aturan perlindungan akun

Admin bisa menonaktifkan, me-logout paksa, mengedit, dan membuka kunci user biasa. Tapi ada batasannya:

- Tidak ada yang bisa mengubah akunnya sendiri lewat `/admin/...`. Untuk akun sendiri, pakai `/me/...`.
- Admin biasa tidak bisa mengelola user yang role-nya ada di `protected_roles` (misalnya admin lain). Itu hanya bisa dilakukan superuser.
- Akun superuser tidak bisa dikelola lewat API sama sekali, termasuk oleh superuser lain. Pakai seeder.
- Admin biasa tidak bisa memberikan role yang dilindungi. Artinya admin tidak bisa membuat admin baru.
- Role superuser tidak bisa diberikan lewat API, hanya lewat seeder.

Dengan begitu, admin tidak bisa saling me-logout atau menonaktifkan, dan tidak ada celah untuk menaikkan jabatan sendiri lewat API.

### Izin mengubah data diri sendiri

Setiap user punya tiga izin: `can_change_username`, `can_change_email`, dan `can_change_password`. Nilai awalnya diambil dari `[account]` di config, dan setelah itu bisa diubah per user oleh admin (lewat `PATCH /admin/users/:id`) atau lewat seeder.

Contoh kasusnya: akun sales yang username-nya sama dengan kode sales di D365 sebaiknya diberi `can_change_username = false`, supaya datanya tidak jadi tidak sinkron.

## Tuning database

Pengaturan pool koneksi ada di `[database]`:

- `max_open_conns` (default 25) adalah batas koneksi per instance. Jumlah koneksi dari semua instance harus di bawah batas database. PostgreSQL misalnya punya `max_connections` default 100, jadi 3 instance x 25 koneksi = 75 masih aman.
- `max_idle_conns` (default 25) sebaiknya disamakan dengan `max_open_conns`, supaya koneksi tidak terus-menerus dibuka dan ditutup.
- `conn_max_lifetime` (default 30 menit) adalah umur maksimal satu koneksi. Turunkan ke 5 sampai 10 menit kalau database berada di belakang PgBouncer atau load balancer yang suka memutus koneksi lama.
- `conn_max_idle_time` (default 5 menit) menutup koneksi yang menganggur saat trafik sepi.

Sebagai gambaran beban: setiap request yang butuh login menjalankan satu query ringan (cek session dan user sekaligus). Login lebih berat, sekitar 3 sampai 4 query ditambah hashing bcrypt yang memakan kira-kira 250 ms CPU. Rate limit di endpoint login ikut melindungi CPU server dari beban ini.

SQLite selalu memakai satu koneksi saja, jadi hanya cocok untuk development.

## Keamanan dan persiapan produksi

Hal-hal ini sudah ditangani di kode:

- Password di-hash dengan bcrypt, panjang maksimalnya 72 byte, dan tidak boleh sama dengan username.
- Respons login gagal dibuat sama persis, baik user-nya tidak ada maupun password-nya salah, supaya orang tidak bisa menebak username mana yang terdaftar. Status "akun nonaktif" baru diberitahukan kalau password-nya benar.
- Ada rate limit per IP dan penguncian per akun setelah beberapa kali salah password. Penguncian ini juga berlaku untuk `current_password` di endpoint `/me`.
- Access token dan refresh token memakai secret yang berbeda. Algoritmanya dikunci ke HS256, dan issuer, audience, serta waktu kedaluwarsanya selalu dicek.
- Session tersimpan di server, sehingga bisa dicabut kapan saja. Refresh token dirotasi dan pemakaian ulangnya terdeteksi. Ada batas waktu tidak aktif dan batas umur maksimal session.
- Di web, refresh token disimpan di cookie HttpOnly, Secure, dan SameSite, ditambah header wajib untuk mencegah CSRF.
- Ganti password, email, atau username harus memasukkan password saat ini. Ganti password juga otomatis me-logout device lain.
- Aturan `protected_roles` dan `superuser_roles` mencegah admin menaikkan jabatan.
- Respons API diberi header keamanan dan `Cache-Control: no-store`. Ukuran body dan waktu request dibatasi, dan setiap request punya ID di log.
- Secret dan token Microsoft tidak pernah dikirim ke aplikasi. Path entity D365 divalidasi, dan `forced_query` bisa dipakai untuk mengunci company.

Sebelum naik ke produksi, cek daftar ini:

- Set `APP_ENV=production`. Di mode ini server menolak start kalau secret masih memakai contoh, `cors_origins` berisi `"*"`, cookie tidak `secure`, atau `rbac.toml` tidak ada.
- Buat secret JWT baru dengan `openssl rand -hex 32`. Simpan di secret manager atau environment variable, jangan di file yang masuk git.
- Pasang HTTPS di depan server (reverse proxy atau load balancer). Isi `proxy_header = "X-Forwarded-For"` dan `trusted_proxies` dengan IP proxy-nya, supaya rate limit membaca IP user yang asli.
- Isi `cors_origins` hanya dengan alamat web yang memang dipakai.
- Ubah `auto_migrate` menjadi `false`, lalu jalankan `./general-auth migrate` sebagai bagian dari proses deploy. Ini supaya beberapa instance tidak menjalankan migrasi bersamaan.
- Aktifkan enkripsi koneksi database: `sslmode=verify-full` untuk Postgres, `tls=true` untuk MySQL, `encrypt=true` untuk SQL Server.
- Pastikan app di Microsoft Entra hanya punya izin seperlunya, user integrasi FO bukan Admin, dan client secret punya tanggal kedaluwarsa serta rutin diganti.
- Periksa lagi isi `rbac.toml`, terutama `[clients]` dan aturan delete.
- Ganti semua password contoh dari seeder, lalu hapus `init/users.json` dari server.
- Rate limit disimpan di memory masing-masing instance. Kalau server dijalankan lebih dari satu instance, tambahkan juga rate limit di gateway atau reverse proxy.

## Struktur project

```
main.go                      perintah: serve, migrate, seed, check, hash-password
example.env                  contoh untuk .env
example.config.toml          contoh untuk config.toml
example.rbac.toml            contoh untuk rbac.toml
example.connector-bc.toml    contoh untuk connector-bc.toml
example.connector-fo.toml    contoh untuk connector-fo.toml
init/users.example.json      contoh untuk init/users.json
internal/config              membaca config, mengganti ${ENV}, validasi
internal/database            koneksi ke berbagai database dan migrasi
internal/store               query ke tabel users dan sessions
internal/auth                JWT, login, refresh, session, lockout, akun, manajemen user
internal/rbac                aturan hak akses
internal/d365                token Microsoft Entra dan client OData ke D365
internal/server              route Fiber, middleware, handler
internal/seed                seeder users.json
```

Untuk menjalankan test: `go test ./...`. Test-nya memakai SQLite di memory dan server D365 tiruan, jadi tidak butuh database atau akses D365 sungguhan. Test ini juga memastikan semua file `example.*` tetap valid.

### Mengubah struktur database

Tambahkan versi migrasi baru di bagian paling akhir daftar `migrations` di `internal/database/dialects.go`, untuk setiap jenis database. Versi yang sudah pernah dirilis jangan diubah.

### Kalau ada masalah

| Gejala | Penyebab yang paling sering |
|---|---|
| `invalid config ... unknown keys` | ada key yang salah ketik di `config.toml`. Bandingkan dengan `example.config.toml`. |
| `jwt.access.secret must be at least 32 characters` | `.env` belum diisi, atau tidak berada di folder yang sama dengan `config.toml` |
| `connect postgres: ...` | database belum jalan, belum dibuat, atau `DB_PORT` masih port database lain |
| `502 d365_unavailable`, di log ada `AADSTS700016` | client ID salah, atau app tidak terdaftar di tenant tersebut |
| di log ada `AADSTS7000215` | client secret salah atau sudah kedaluwarsa |
| D365 terus membalas 401 | app belum didaftarkan di halaman Microsoft Entra Applications di BC/FO, atau scope-nya salah |
| D365 membalas 403 | permission set (BC) atau security role user integrasi (FO) kurang |
| Web: refresh selalu gagal atau cookie tidak terkirim | lupa `credentials: "include"`, lupa header `X-Requested-With`, atau domain web dan API berbeda sementara cookie memakai `SameSite=Strict` |
| Mobile tiba-tiba dapat `refresh_token_reused` | refresh dipanggil dua kali bersamaan, atau refresh token baru tidak disimpan |
