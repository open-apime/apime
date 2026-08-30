[🇧🇷 Leia em Português](README.md)

Go API to orchestrate multiple instances.

Based on the [WhatsMeow](https://github.com/tulir/whatsmeow) library with dashboard and Webhook events.

<img src="docs/dashboard.png" />
<img src="docs/dashboard_light.png" />

1. **Configure the environment:**
   ```bash
   cp .env.example .env
   ```
   *(Edit the `.env` as needed).*

2. **Start the containers:**
   ```bash
   docker compose up -d
   ```
   **Important:** Migrations and the initial admin user are created automatically on first boot.

## Access

- **Dashboard:** `http://localhost:8080/dashboard`
- **Email:** `admin@apime.local`
- **Password:** `admin123`


- **API Specification:** `openapi.yaml`

## Official SDK (Node/TypeScript)

Typed client for Node integrations, with retry, webhook verification and all 66 operations of the
API: [`@open-apime/sdk`](https://www.npmjs.com/package/@open-apime/sdk).

```bash
npm install @open-apime/sdk
```

```ts
import { Apime } from "@open-apime/sdk";

const connection = Apime.withInstanceToken(
  { token: process.env.INSTANCE_TOKEN!, instanceId: "abc-123" },
  { baseUrl: "https://apime.example.com" },
);

await connection.messages.sendText(
  { to: "5511999999999", text: "Hello" },
  { idempotencyKey: message.id },
);
```

Not on Node? The API is plain HTTP: `openapi.yaml` describes every route.

## Documentation

The pages below are written in Portuguese. `openapi.yaml` is the language-neutral reference.

| Doc | Subject |
|---|---|
| [docs/dashboard.md](docs/dashboard.md) | web interface, authentication and admin routes |
| [docs/webhook-payloads.md](docs/webhook-payloads.md) | envelope, HMAC signature and the event types |
| [docs/idempotency.md](docs/idempotency.md) | repeating a send without duplicating the message |
| [docs/users.md](docs/users.md) | users and tokens |
| [docs/media.md](docs/media.md) | media |
| [docs/phone-numbers.md](docs/phone-numbers.md) | numbers and JIDs |
| [docs/whatsapp-advanced.md](docs/whatsapp-advanced.md) | groups, newsletters and privacy |
| [docs/health-check.md](docs/health-check.md) | health check |
