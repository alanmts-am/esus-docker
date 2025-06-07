## e-SUS Docker

Projeto que reuni um pouco de conhecimento adquirido em vários lugares, em como rodar o projeto e-SUS e aplicado ao conceito de Docker no Linux.

## Como rodar

Antes de mais nada, lembre-se de criar um arquivo .env com base no template do projeto. Assim o sistema saberá a versão do e-SUS para baixar.

Para testar o projeto, rode o comando abaixo

```bash
sudo docker compose -f docker-compose.db.yml up -d
```

Se você já tem um banco de dados Postgres na versão 9.6 ou superior, pode usar o comando abaixo

```Bash
sudo docker compose -f docker-compose.yml up -d
```
Lembrando que para funcionar, devem ser descomentadas e passadas todas as variáveis do arquivo .env

```bash
POSTGRES_HOST=
POSTGRES_PORT=
POSTGRES_DB=
POSTGRES_USER=
POSTGRES_PASSWORD=
```

Por padrão, dadas as configurações do compose, a aplicação será iniciada em http://localhost:8081

O banco de dados fica acessível pela porta 5433.
