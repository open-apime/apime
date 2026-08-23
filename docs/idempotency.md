# Idempotência no envio

Repetir um envio que talvez já tenha dado certo é o pior tipo de dúvida: se a resposta se perdeu
no caminho, o chamador não sabe se a mensagem chegou ao WhatsApp. Sem uma chave, a saída é escolher
entre reenviar (e arriscar mensagem duplicada para o cliente final) ou desistir (e perder a
mensagem). A `Idempotency-Key` remove a escolha.

Segue o desenho de `draft-ietf-httpapi-idempotency-key-header`.

## Como usar

Mande um header `Idempotency-Key` com um valor único por envio, gerado por você. UUID v4 serve.

```bash
curl -X POST http://localhost:8080/api/instances/$ID/messages/text \
  -H "Authorization: Bearer $INSTANCE_TOKEN" \
  -H "Idempotency-Key: 0f9d2c7e-51a1-4f0e-9b6f-2c0a5b1d3e4f" \
  -H "Content-Type: application/json" \
  -d '{"to":"5511999999999","text":"Olá"}'
```

Repetindo a chamada com a mesma chave, a API devolve **o resultado guardado**, com o mesmo
`messageId`, e marca a resposta com `X-Idempotent-Replay: true`. A mensagem sai uma vez só.

**Sem o header, nada muda.** Quem não manda chave continua se comportando exatamente como antes.

## Onde vale

Nas sete rotas de envio: `messages`, `messages/text`, `messages/media`, `messages/audio`,
`messages/document`, `messages/contact` e `messages/location`. Em `GET` não faz sentido, porque
leitura já é idempotente por natureza.

## Respostas

| Situação | Resposta |
|---|---|
| primeira chamada | resultado normal do envio |
| repetição, mesma chave e mesmo conteúdo | resultado guardado, com `X-Idempotent-Replay: true` |
| repetição enquanto a primeira ainda roda | `409`, pode tentar de novo depois |
| mesma chave com conteúdo diferente | `422`, corrija a chave ou o conteúdo |
| chave acima de 255 caracteres | `400` |

## O que é guardado, e por quanto tempo

O status e o corpo da resposta, por **24 horas**. Depois disso a chave expira e uma repetição vira
envio novo. Uma faxina roda de hora em hora removendo o que expirou.

Erro `4xx` **não é guardado**: a mensagem não chegou a sair, então a chave é liberada e você pode
corrigir o conteúdo e repetir com a mesma chave.

Erro `5xx` **é guardado** e repetido como qualquer outro resultado. Isso é proposital: um 500 é
ambíguo, a mensagem pode ter chegado ao WhatsApp antes da falha, e reenviar seria justamente o
risco que a chave existe para evitar.

**`503` é a exceção**, e também libera a chave. O envio devolve 503 com "sessão não pronta, tente
novamente" antes de chegar ao WhatsApp, então nada saiu. Guardar seria responder toda repetição com
o mesmo 503 até a chave expirar, e a instrução de tentar de novo ficaria impossível de cumprir.

## Limites

- Se o banco estiver indisponível, o envio **passa assim mesmo**, sem a proteção. Perder a
  idempotência é ruim, derrubar o envio é pior.
- Em `multipart` (mídia, áudio, documento) a impressão digital do conteúdo não inclui o arquivo,
  para não segurar um upload de até 75 MB em memória. A proteção contra envio duplicado continua
  valendo; só a checagem de "mesma chave com conteúdo diferente" fica mais fraca nessas rotas.
