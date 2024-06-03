Primeiros testes com GO e com Waze para notificar os usuários baseado nos alertas do Waze em determinada área ou região.


Em broadcastFeedURL substituia  depois de buid=xxxxxxxxxx, pela sua ID adqurida no Waze.
Em telegramBotToken insira as credenciais do seu Bot no Telegram
Em telegramChatID insira a ID do canal criado com seu bot para entrega das mensagens.

Esse aplicativo ainda está em caráter de testes, e com certeza pode ser melhorado.

O arquivo driver.go possui o código com a estrutura de notificação por console
O arquivo waze.go possui o código com a estrutura de notificação através do navegador

Toda a estrutura ainda está rústica, e pode ser melhorada e muito.
