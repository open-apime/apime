# Changelog

## [v1.0.2] - 2026-01-17
### Adicionado
- Endpoint raiz agora responde com status, versão e nome da aplicação, enquanto o dashboard passou a receber `config.Version` para exibir a build atual.
- Nova variável `MEDIA_TTL_SECONDS` documentada no `.env.example` e nos READMEs para ajustar o TTL de mídias armazenadas via configuração.
- Captura de tela em modo claro (`docs/dashboard_light.png`) adicionada à documentação para destacar o novo tema visual do dashboard.

### Atualizado
- Fluxo de inicialização da API e do manager do WhatsMeow suporta DSN explícito para PostgreSQL, expõe diagnósticos de armazenamento e adiciona a dependência `github.com/lib/pq` para compatibilidade.
- Página "Docs" do dashboard recebeu um redesenho completo, com navegação lateral interativa, modos claro/escuro aprimorados e blocos de código expansíveis.
- O `docker-compose.scalable.yml` ganhou healthchecks para Postgres/Redis, rede dedicada e dependências condicionais para garantir subida ordenada dos serviços.

### Corrigido
- Logs do pool de webhooks agora incluem prefixos por worker e mensagens consistentes em todo o fluxo, facilitando o troubleshooting de entregas.
- Consultas de history sync e instances convertem `history_sync_cycle_id` para texto, evitando erros de tipo no Postgres.

## [v1.0.1] - 2026-01-17
### Adicionado
- Changelog inicial seguindo o ponto de partida marcado pelo tag `v1.0.0`.

### Corrigido
- Ativação do modo `ManualHistorySyncDownload` para liberar o dispositivo imediatamente após o pareamento e evitar travamentos na tela de sincronização do QR code.
- Worker de history sync simplificado para concluir ciclos sem bloquear o login enquanto o modo manual está ativo.
- Logs mais claros sobre o estado da instância (conexão, sincronização crítica e presença) para facilitar o diagnóstico do fast login.

### Internals
- Limpeza do pipeline de eventos de History Sync para impedir tentativas de desserialização inválidas e reduzir ruído de erros.

## [v1.0.0] - 2026-01-17
- Versão inicial publicada.
