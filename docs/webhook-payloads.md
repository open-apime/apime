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

São 15 tipos entregues ao consumidor. `ignore` existe no código mas é descartado antes da entrega.

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
| `call.offer` | ligação recebida |
| `call.ended` | ligação encerrada, recusada ou atendida em outro aparelho |
| `call.state` | mudança de fase da ligação no motor de chamadas (só com `calls_enabled`) |
| `call.accepted` | ligação atendida (só com `calls_enabled`) |
| `call.video` | câmera do outro lado ligou, desligou ou pediu vídeo (só com `calls_enabled`) |
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
| `isBroadcast` | `true` quando a mensagem vem de ou vai para lista de transmissão. Só presente nesse caso (abaixo) |
| `isStatusBroadcast` | `true` quando é story/status, e não lista de transmissão |
| `broadcastListOwner` | JID de quem enviou pela lista. É o campo das mensagens **recebidas** |
| `broadcastRecipients` | Array de `{pn, lid}` com todos os destinatários. Só nas mensagens **enviadas** por você |

**Lista de transmissão.** A lista é local do remetente: quem recebe vê uma conversa 1:1 comum e
nunca sabe que a lista existe. **O WhatsApp emite UM evento para a lista inteira, não um por
destinatário.** Então o consumidor precisa fazer fanout por `broadcastRecipients` e registrar a
mensagem em cada conversa 1:1, uma por destinatário. Tratar o evento como uma mensagem só deixa a
conversa de todo mundo, menos a do primeiro, sem o registro. `isStatusBroadcast: true` separa o
caso de story/status, que não tem lista e não pede fanout.

**Os dois sentidos trazem campos diferentes.** `broadcastRecipients` só vem quando a mensagem é
sua (`isFromMe: true`), porque é o único caso em que o WhatsApp informa a lista de destinatários.
Numa mensagem **recebida** por uma lista de terceiro o array não vem, e quem identifica a conversa
é `from` junto de `broadcastListOwner`. Fanout, portanto, só no sentido de saída.

**`isGroup` também vem `true` aqui.** O whatsmeow trata lista e grupo no mesmo ramo, então quem
decide pelo `isGroup` primeiro cai no caminho de grupo. Para separar, teste `isBroadcast` antes.

**O mesmo `messageId` em todos.** O WhatsApp gera um id só para a lista inteira, então as N cópias
do fanout repetem esse valor. Quem tiver unicidade por `messageId` precisa compor a chave com o
destinatário, ou perde todas as cópias menos a primeira.

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
a conexão para `connected`.

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

### Chamadas: `call.offer`, `call.ended`, `call.state`, `call.accepted`, `call.video`

`call.offer` sai **em toda instância com webhook**, mesmo sem o motor de chamadas, para o
consumidor registrar a ligação e, se quiser, recusar sozinho. Os demais dependem do motor
(`calls_enabled: true`, ver `PUT /instances/{id}`); sem ele, o fim chega como `call.ended` com o
motivo cru do WhatsApp.

Campos de `call.offer`:

| Campo        | Descrição                                                              |
|--------------|------------------------------------------------------------------------|
| `callId`     | id da ligação, a chave de todas as rotas `/calls/{callId}`              |
| `from`       | JID de telefone de quem ligou, quando resolvido                         |
| `lid`        | LID de quem ligou, quando a ligação veio endereçada por LID             |
| `timestamp`  | quando o servidor do WhatsApp registrou o evento                        |
| `rejectFrom` | JID a passar em `POST /calls/{callId}/reject` numa instância sem motor  |
| `isVideo`    | ligação de vídeo                                                        |
| `platform`   | plataforma do aparelho de quem ligou                                    |
| `isGroup`    | ligação começada num grupo; `groupJid` diz qual                         |
| `joinable`   | mais pessoas podem entrar, mesmo fora de grupo                          |

Os eventos do motor trazem `callId`, `from`, `lid`, `direction` (`incoming` ou `outgoing`),
`state` (`calling`, `ringing`, `connecting`, `active`, `ended`), `startedAt`, `isVideo`, `isGroup`
e `groupJid`:

- `call.state`: cada mudança de fase. O fim também passa aqui, com `state: ended` e `reason`.
- `call.accepted`: a ligação foi atendida (a feita, pelo outro lado; a recebida, por `answer`).
- `call.video`: campo `video` com `on`, `off` ou `upgrade_requested` (o outro lado pediu para
  passar a voz para vídeo; aceite com `POST /calls/{callId}/video` e `action: accept`).
- `call.ended`: **uma vez por ligação**, com `reason`:

| `reason` | Quando |
|---|---|
| `missed` | ninguém atendeu |
| `rejected` | o outro lado recusou |
| `rejected_elsewhere` | esta conta recusou em outro aparelho |
| `accepted_elsewhere` | esta conta atendeu em outro aparelho |
| `cancelled` | este lado desistiu antes de atenderem |
| `hangup` | encerrada depois de atendida |
| `group_call_ended` | a ligação de grupo acabou |

> Trate os eventos de chamada como idempotentes por `callId`. O conteúdo bruto do evento (`raw`)
> **não** é enviado, porque a oferta carrega a chave cifrada da ligação.

---

### `unknown`
Evento do whatsmeow que o normalizador ainda não mapeia. Serve para não perder sinal.

---

### `ignore` (interno)
Não é entregue. O dispatcher descarta antes de enfileirar, para eventos que não interessam ao
consumidor (mensagem indecifrável, sync de contato sem username).
