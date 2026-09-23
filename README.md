# go-pdf-forge

Current version: **v0.0.7** — fondation pré-alpha, le parcours PDF complet est
encore en cours d'implémentation.

Service cloud-native de traitement asynchrone de PDF, écrit en Go, avec une
interface Vue/TypeScript. Le service accepte les envois découpés, traite les
documents avec Ghostscript et expose l'avancement sans conserver durablement
les fichiers.

Le projet est en construction. Les décisions stabilisées et les limites de
sécurité sont décrites dans [ARCHITECTURE.md](ARCHITECTURE.md). Le brief source
se trouve dans [docs/brief_go_pdf_self_service_agent.md](docs/brief_go_pdf_self_service_agent.md)
et le plan d'action suivi dans [docs/ROADMAP.md](docs/ROADMAP.md).
Le [contrat de qualité documentaire](docs/DOCUMENTATION.md) s'applique aux
contributeurs humains comme aux agents, avec un inventaire navigable de tous
les fichiers dans [docs/atlas.md](docs/atlas.md).

## Qualité documentaire

Le contrat décrit précisément les commentaires attendus pour les packages et
API Go, les contrats Protobuf, la responsabilité fichier par fichier, les
promesses documentaires exécutables et leur intégration à la release. Il sert
aussi de modèle d'adoption pour d'autres dépôts Go.

`make docs-check` compose trois contrôles : GoDoc via `cmd/doccheck`, exactitude
bidirectionnelle de l'atlas via le même outil, puis cohérence des promesses
stables via `scripts/check_documentation_claims.sh`. Cette garde est reprise par
`make check`, la CI et `make release-check`; une release ne peut donc pas
utiliser une politique documentaire plus faible que le développement local.

## Prérequis de développement

- Go 1.27.1
- Buf
- ripgrep (`rg`), utilisé par les gardes de format et de documentation
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
make docs-check     # contrôle GoDoc, atlas et promesses documentées
make check          # format, lint, tests, vet et documentation
make build          # construit les binaires présents dans cmd/
make release-check  # garde complète avant commit/tag
```

Les migrations SQL sont sous `db/migrations`. Elles ne sont pas appliquées par
`make check` ou au démarrage d'un service. Leur exécution sur une base réelle
est une opération explicite :

```bash
go run ./cmd/pdf-migrate --list  # aucune connexion PostgreSQL
go run ./cmd/pdf-migrate --check # configuration + connexion, sans mutation
go run ./cmd/pdf-migrate --up    # connexion et mutation explicites
```

Le runner embarqué sérialise les exécutions, vérifie le checksum de l'historique
et applique toutes les migrations pendantes dans une transaction.

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
versionnée de `CHANGELOG.md` dans le même commit. Toute tâche `GPF-*` terminée
doit être citée explicitement dans une section versionnée du changelog ; la
garde de release vérifie aussi qu'une tâche annoncée comme livrée est cochée
dans la roadmap.

```bash
make release-prepare
git add <fichiers-relus>
git diff --cached --check
git commit -m "chore(release): prepare v0.0.7"
CONFIRM_RELEASE=v0.0.7 make release
```

La dernière commande exige une branche `main` propre, recrée tous les contrôles,
pose un tag annoté et pousse `main` et le tag atomiquement. Le push du tag lance
la création de la GitHub Release à partir de la section correspondante du
changelog.
