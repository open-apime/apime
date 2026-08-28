# Payloads de Webhook

## Estrutura Base
```json
{
  "id": "uuid-do-evento",
  "instanceId": "id-da-instancia",
  "type": "tipo-do-evento",
  "payload": { ... },
  "createdAt": "2024-01-01T12:00:00Z"
}
```

---

## Segurança (Assinatura)

Se um `webhook_secret` for definido na instância, o ApiMe envia um hash HMAC-SHA256 no header `X-ApiMe-Signature`.
Para validar, gere o HMAC-SHA256 do corpo da requisição usando seu secret e compare com o header.

O corpo assinado é o **corpo cru**, antes de qualquer parse de JSON. Validar sobre o objeto já
parseado e reserializado quebra a assinatura.

---

## Tipos de Eventos

São 12 tipos entregues ao consumidor. `ignore` existe no código mas é descartado antes da entrega.

| Tipo | Quando |
|---|---|
| `message` | mensagem recebida ou enviada |
| `receipt` | confirmação de entrega ou leitura |
| `presence` | contato ficou online ou offline |
| `chat_presence` | contato está digitando ou gravando |
| `reaction` | reação a uma mensagem |
| `contact_update` | contato sincronizado ganhou @username |
| `connected` | instância conectou |
| `disconnected` | instância desconectou ou deslogou |
| `temporary_ban` | conta banida temporariamente, ou reach-out travado |
| `restriction_lifted` | restrição anterior saiu |
| `contact_reachout_locked` | envio bloqueado para um contato específico (463) |
| `unknown` | evento não mapeado |

---

### `message`
Mensagem recebida (texto, imagem, áudio, vídeo, documento, sticker, contato ou localização).

| Campo       | Descrição                                      |
|-------------|------------------------------------------------|
| `from`      | JID do remetente                               |
| `to`        | JID do destinatário (chat)                     |
| `isFromMe`  | `true` se enviado pela própria instância       |
| `isGroup`   | `true` se for mensagem de grupo                |
| `messageId` | ID único da mensagem no WhatsApp               |
| `timestamp` | Timestamp Unix                                 |
| `pushName`  | Nome do remetente                              |
| `text`      | Conteúdo (para texto)                          |
| `mediaType` | `image`, `video`, `audio`, `document`, `sticker`, `location`, `contact` |
| `mediaUrl`  | URL local para download da mídia (pré-baixada) |
| `mimetype`  | Tipo MIME do arquivo                           |
| `caption`   | Legenda (imagem/vídeo)                         |
| `buttons`   | Botões, quando a mensagem é interativa (abaixo) |
| `editedMessageId` | Id da mensagem **original**, quando este evento é uma edição (abaixo) |
| `editedText` | Novo texto da mensagem editada |

**Edição de mensagem.** O WhatsApp entrega edição como `secretEncryptedMessage` com
`SecretEncType = MESSAGE_EDIT`, não mais como `protocolMessage` tipo 14. O apime decifra e expõe
`editedMessageId` (a mensagem que foi editada) e `editedText` (o conteúdo novo). **Os dois vêm
sempre juntos:** mandar só o id daria ao consumidor uma edição sem nada para aplicar. Não havendo
os campos, é mensagem comum.

**Botões.** Cada item de `buttons` tem `id`, `label` e `type`, onde `type` vale `reply`, `url`,
`copy` ou `call`. O de `url` traz `url`, o de `copy` traz `code`, e o de `call` traz `phone`.
São tipos de **botão**, não de evento.

---

### `receipt`
Confirmação de entrega ou leitura.

| Campo          | Descrição                                        |
|----------------|--------------------------------------------------|
| `messageIds`   | Array de IDs confirmados                         |
| `timestamp`    | Timestamp da confirmação                         |
| `chat`         | JID do chat                                      |
| `status`       | `read`, `delivered` ou `played`                  |

---

### `presence`
Mudanca de status online/offline.

| Campo        | Descrição                      |
|--------------|--------------------------------|
| `from`       | JID do contato                 |
| `unavailable`| `true` = offline               |
| `lastSeen`   | Última vez online (se offline) |

---

### `chat_presence`
Contato digitando ou gravando áudio dentro de um chat.

| Campo     | Descrição                                  |
|-----------|--------------------------------------------|
| `from`    | JID de quem está digitando                 |
| `chatJID` | JID do chat                                |
| `state`   | `composing` ou `paused`                    |
| `media`   | vazio para texto, `audio` para gravação    |

---

### `reaction`
Reação adicionada ou removida de uma mensagem.

---

### `contact_update`
Sincronização de contato que trouxe o @username do WhatsApp.

| Campo      | Descrição                 |
|------------|---------------------------|
| `jid`      | JID do contato            |
| `username` | @username do WhatsApp     |

Só é emitido quando há username. Sem ele o evento vira `ignore` e não sai, para um sync completo
de contatos não inundar o webhook.

---

### `connected`
A instância conectou ao WhatsApp.

---

### `disconnected`
A instância desconectou do WhatsApp. Em logout, traz `reason`.

---

### `temporary_ban`
A conta foi restringida. Cobre dois casos, com o mesmo tipo de propósito: o consumidor que já
trata `temporary_ban` cobre os dois de graça.

| Campo             | Descrição                                                      |
|-------------------|----------------------------------------------------------------|
| `reason`          | motivo textual                                                 |
| `code`            | código do servidor (463 no caso do reach-out timelock)          |
| `active`          | `true` enquanto a restrição vale                               |
| `restrictedUntil` | data RFC3339 de quando expira, quando o servidor informa       |
| `enforcementType` | tipo de aplicação, quando informado                            |

Sem este evento a restrição só aparecia no log do apime, e a conexão seguia "connected" no
consumidor enquanto todo envio falhava.

---

### `restriction_lifted`
A restrição anterior saiu e a conta voltou ao normal. Traz `active: false`. O consumidor devolve
a conexao para `connected`.

---

### `contact_reachout_locked`
O envio para **um contato específico** foi bloqueado (erro 463), tipicamente contato frio que
ainda não iniciou conversa. Não restringe a conta inteira.

| Campo    | Descrição                          |
|----------|------------------------------------|
| `to`     | JID do contato bloqueado           |
| `reason` | `server returned error 463`        |
| `detail` | explicação do bloqueio             |
| `code`   | `463`                              |

---

### `unknown`
Evento do whatsmeow que o normalizador ainda não mapeia. Serve para não perder sinal.

---

### `ignore` (interno)
Não é entregue. O dispatcher descarta antes de enfileirar, para eventos que não interessam ao
consumidor (mensagem indecifrável, sync de contato sem username).
