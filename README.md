## e-SUS Docker

Projeto que reúne o conhecimento necessário para rodar o e-SUS PEC em containers Docker no Linux.

## Como rodar

Antes de mais nada, crie um arquivo `.env` com base no `.env.template`. Ele define a versão do e-SUS, a URL de download e o caminho dos volumes.

> Não altere as variáveis `ESUS_DOWNLOAD_URL` e `ESUS_IMAGE` sem ter certeza — isso pode quebrar o build.

### Setup simples (app + banco)

Apenas o e-SUS PEC e o banco de dados Postgres. Ideal para testes rápidos.

```bash
sudo docker compose -f docker-compose.db.yml up -d
```

### Setup completo (app + banco + workers)

Inclui, além do e-SUS e do banco, uma pipeline de auditoria de SQL:

- **watcher** — lê os logs do container do Postgres via Docker socket, intercepta as queries SQL executadas pelo e-SUS (reconstruindo os valores dos parâmetros no lugar dos `$1`, `$2`...) e publica cada query no Redis
- **redis** — fila que desacopla o watcher do consumer
- **consumer** — consome as queries do Redis e as persiste num banco Postgres separado (`watcher`), com a tabela `queries` contendo: PID da conexão, texto da query, timestamp e tabela afetada

```bash
sudo docker compose up -d
```

## Portas

| Serviço     | Host  | Container |
|-------------|-------|-----------|
| e-SUS (HTTP)| 8081  | 8080      |
| e-SUS       | 8082  | 80        |
| e-SUS (TLS) | 8083  | 443       |
| Postgres    | 5543  | 5432      |
