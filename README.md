# go-pdf-forge

Current version: **v0.0.1** — fondation pré-alpha, le parcours PDF complet est
encore en cours d'implémentation.

Service cloud-native de traitement asynchrone de PDF, écrit en Go, avec une
interface Vue/TypeScript. Le service accepte les envois découpés, traite les
documents avec Ghostscript et expose l'avancement sans conserver durablement
les fichiers.

Le projet est en construction. Les décisions stabilisées et les limites de
sécurité sont décrites dans [ARCHITECTURE.md](ARCHITECTURE.md). Le brief source
se trouve dans [docs/brief_go_pdf_self_service_agent.md](docs/brief_go_pdf_self_service_agent.md).

## Prérequis de développement

- Go 1.27.1
- Buf
- PostgreSQL
- un stockage compatible S3 (Garage en développement)
- Ghostscript et les outils Poppler (`pdfinfo`, `pdfimages`)

La base locale `go_pdf_forge` et Garage restent des services externes au
processus Go. Le fichier `.env` local n'est jamais chargé automatiquement par
les commandes du dépôt et ne doit pas être commité.

## Commandes

```bash
make generate       # génère les clients/serveurs Go et TypeScript
make test           # lance les tests Go
make check          # format, lint Protobuf, tests et go vet
make build          # construit les binaires présents dans cmd/
make release-check  # garde complète avant commit/tag
```

Les migrations SQL sont sous `db/migrations`. Elles ne sont pas appliquées par
`make check`; leur exécution sur une base réelle est une opération explicite.

## POC SMTP

Le diagnostic SMTP sait utiliser un relais en clair, STARTTLS ou TLS implicite.
Il n'effectue aucun basculement automatique entre fournisseurs.

Validation sans connexion réseau :

```bash
go run ./cmd/mail-poc \
  --smtp-host smtp.example.test \
  --smtp-port 25 \
  --mode plain \
  --from pdf@example.test \
  --to user@example.test \
  --dry-run
```

Pour un serveur authentifié, le mot de passe est fourni uniquement via
`SMTP_PASSWORD`. Il n'est jamais affiché. Ne pas proposer de notification par
email aux sessions anonymes.

## Préparer une release

La version dans `internal/version/version.go` est la source de vérité. Chaque
release doit mettre à jour cette constante, le bandeau du README et une section
versionnée de `CHANGELOG.md` dans le même commit.

```bash
make release-prepare
git add <fichiers-relus>
git diff --cached --check
git commit -m "chore(release): prepare v0.0.1"
CONFIRM_RELEASE=v0.0.1 make release
```

La dernière commande exige une branche `main` propre, recrée tous les contrôles,
pose un tag annoté et pousse `main` et le tag atomiquement. Le push du tag lance
la création de la GitHub Release à partir de la section correspondante du
changelog.
