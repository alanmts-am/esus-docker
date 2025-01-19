## e-SUS Docker

Projeto que reuni um pouco de conhecimento adquirido em vários lugares, em como rodar o projeto e-SUS e aplicado ao conceito de Docker no Linux.

## Como rodar

```Bash
sudo docker compose -f docker-compose.yml up -d
```

Use a flag --build para recriar a imagem, caso necessário.

!! A imagem do sistema esus deve ser baixada e colocada na raiz do projeto, por hora, com o nome "pec.jar"

Por padrão, dadas as configurações do compose, a aplicação será iniciada http://localhost:8081

O banco de dados fica acessível pela porta 5433.


## O que teria de melhorar

- [ ] Baixar e enviar automaticamente a imagem e-SUS para o Docker