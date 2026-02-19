module github.com/open-apime/apime

go 1.25.0

require (
	github.com/caarlos0/env/v10 v10.0.0
	github.com/gin-contrib/cors v1.7.6
	github.com/gin-gonic/gin v1.10.1
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.8.0
	github.com/lib/pq v1.11.2
	github.com/mattn/go-sqlite3 v1.14.34
	github.com/redis/go-redis/v9 v9.17.3
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	go.mau.fi/whatsmeow v0.0.0-20260219150138-7ae702b1eed4
	go.uber.org/zap v1.27.1
	golang.org/x/crypto v0.48.0
	google.golang.org/protobuf v1.36.11
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/beeper/argo-go v1.1.2 // indirect
	github.com/bytedance/sonic v1.11.8 // indirect
	github.com/bytedance/sonic/loader v0.4.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/elliotchance/orderedmap/v3 v3.1.0 // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/gin-contrib/sse v1.1.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.1 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/petermattis/goid v0.0.0-20260113132338-7c7de50cc741 // indirect
	github.com/rogpeppe/go-internal v1.10.0 // indirect
	github.com/rs/zerolog v1.34.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.3.1 // indirect
	github.com/vektah/gqlparser/v2 v2.5.31 // indirect
	go.mau.fi/libsignal v0.2.1 // indirect
	go.mau.fi/util v0.9.6 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/arch v0.23.0 // indirect
	golang.org/x/exp v0.0.0-20260212183809-81e46e3db34a // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Exclude versões problemáticas que removem pacotes internos
// A partir da v1.11.9, o sonic removeu pacotes internos necessários
exclude (
	github.com/bytedance/sonic v1.11.9
	github.com/bytedance/sonic v1.12.0
	github.com/bytedance/sonic v1.12.1
	github.com/bytedance/sonic v1.12.2
	github.com/bytedance/sonic v1.12.3
	github.com/bytedance/sonic v1.12.4
	github.com/bytedance/sonic v1.12.5
	github.com/bytedance/sonic v1.12.6
	github.com/bytedance/sonic v1.12.7
	github.com/bytedance/sonic v1.12.8
	github.com/bytedance/sonic v1.12.9
	github.com/bytedance/sonic v1.12.10
	github.com/bytedance/sonic v1.13.0
	github.com/bytedance/sonic v1.13.1
	github.com/bytedance/sonic v1.13.2
	github.com/bytedance/sonic v1.13.3
	github.com/bytedance/sonic v1.14.0
	github.com/bytedance/sonic v1.14.1
	github.com/bytedance/sonic v1.14.2
	github.com/gin-gonic/gin v1.11.0
)
