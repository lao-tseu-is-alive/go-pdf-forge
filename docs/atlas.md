# Atlas du dépôt go-pdf-forge

Version suivie : **v0.0.9**.

Cet index attribue à chaque fichier non ignoré une responsabilité explicite.
Les chemins sont contrôlés dans les deux directions par `make atlas-check`.

## Gouvernance et automatisation

- `.env.example` — Exemple sans secret des variables de configuration prises en charge.
- `.github/workflows/ci.yml` — Pipeline GitHub Actions de validation continue du dépôt.
- `.github/workflows/release.yml` — Publication GitHub déclenchée par les tags de version.
- `.gitignore` — Règles excluant secrets, sorties de build et artefacts locaux.
- `AGENTS.md` — Instructions durables destinées aux agents travaillant dans ce dépôt.
- `ARCHITECTURE.md` — Décisions d’architecture, frontières de sécurité et flux de référence.
- `CHANGELOG.md` — Historique versionné des changements livrés et à venir.
- `LICENSE` — Licence BSD à trois clauses du projet.
- `Makefile` — Point d’entrée reproductible pour génération, contrôles, builds et releases.
- `README.md` — Présentation du projet et parcours opérateur principal.
- `buf.gen.yaml` — Configuration de génération des bindings Protobuf Go et TypeScript.
- `buf.yaml` — Module Buf et règles de lint des contrats Protobuf.
- `go.mod` — Déclaration du module Go et de ses dépendances directes.
- `go.sum` — Sommes cryptographiques des dépendances Go résolues.

## Contrats API et bindings générés

- `api/pdfjob/v1/pdf_job.proto` — Contrat Protobuf de consultation et de pilotage des jobs PDF.
- `api/session/v1/session.proto` — Contrat Protobuf de création et de révocation des sessions anonymes.
- `api/upload/v1/upload.proto` — Contrat Protobuf des uploads découpés et idempotents.
- `gen/go/pdfjob/v1/pdf_job.pb.go` — Binding Go généré depuis le contrat des jobs PDF ; ne jamais modifier à la main.
- `gen/go/pdfjob/v1/pdfjobv1connect/pdf_job.connect.go` — Stubs ConnectRPC Go générés pour le service des jobs PDF.
- `gen/go/session/v1/session.pb.go` — Binding Go généré depuis le contrat des sessions ; ne jamais modifier à la main.
- `gen/go/session/v1/sessionv1connect/session.connect.go` — Stubs ConnectRPC Go générés pour le service des sessions.
- `gen/go/upload/v1/upload.pb.go` — Binding Go généré depuis le contrat des uploads ; ne jamais modifier à la main.
- `gen/go/upload/v1/uploadv1connect/upload.connect.go` — Stubs ConnectRPC Go générés pour le service des uploads.
- `web/src/gen/pdfjob/v1/pdf_job_pb.ts` — Binding TypeScript généré depuis le contrat des jobs PDF.
- `web/src/gen/session/v1/session_pb.ts` — Binding TypeScript généré depuis le contrat des sessions.
- `web/src/gen/upload/v1/upload_pb.ts` — Binding TypeScript généré depuis le contrat des uploads.

## Commandes Go

- `cmd/doccheck/main.go` — Vérificateur déterministe des contrats GoDoc et de l’atlas du dépôt.
- `cmd/doccheck/main_test.go` — Tests positifs et négatifs du vérificateur documentaire.
- `cmd/mail-poc/main.go` — Diagnostic explicite de connectivité et de remise SMTP.
- `cmd/pdf-migrate/main.go` — Commande explicite de contrôle et d’application des migrations PostgreSQL.

## Persistance

- `db/migrations/20260915170000_initial.sql` — Schéma PostgreSQL initial des sessions, uploads, jobs, quotas et outbox.
- `db/migrations/20260917091500_anonymous_session_digest_lengths.sql` — Contraintes garantissant des digests HMAC complets pour les sessions anonymes.
- `db/migrations/20260923100000_anonymous_ip_usage.sql` — Agrégats atomiques de quotas par empreinte IP et index des agrégats par session.
- `db/migrations/20260923110000_upload_integrity_constraints.sql` — Contraintes SQL de cohérence des métadonnées, états et SHA-256 d’upload.
- `db/migrations/20260924100000_pdf_job_repository.sql` — Colonnes et contraintes de cohérence nécessaires au repository durable des jobs.
- `db/migrations/embed.go` — Embarquement typé des migrations SQL dans les binaires Go.
- `internal/database/database.go` — Ouverture, validation et observation non sensible du pool PostgreSQL.
- `internal/database/database_test.go` — Tests unitaires de configuration et de cycle de vie du pool.
- `internal/database/migrate.go` — Chargement, checksum et exécution transactionnelle des migrations.
- `internal/database/migrate_test.go` — Tests du catalogue, des checksums et du comportement du migrateur.

## Domaine et infrastructure interne

- `internal/anonymous/token.go` — Génération, analyse et comparaison sûre des capacités anonymes.
- `internal/anonymous/token_test.go` — Tests de sécurité et de validation des capacités anonymes.
- `internal/anonymous/session.go` — Cycle de vie métier des sessions anonymes persistantes.
- `internal/anonymous/session_test.go` — Tests de création, authentification, expiration et révocation anonymes.
- `internal/anonymous/quota.go` — Politique de quotas anonymes, fenêtres UTC et dérivation des empreintes IP.
- `internal/anonymous/quota_test.go` — Tests des limites, fenêtres et consommations métier des quotas anonymes.
- `internal/anonymous/storage_quota_postgres.go` — Transactions pgx atomiques des compteurs de quotas par session et IP.
- `internal/anonymous/storage_quota_postgres_integration_test.go` — Test PostgreSQL opt-in de concurrence et de respect exact des quotas.
- `internal/anonymous/storage_quota_postgres_test.go` — Tests d’ordre de verrouillage, dépassement et rollback des quotas PostgreSQL.
- `internal/anonymous/storage_postgres.go` — Repository pgx des sessions anonymes ne manipulant que des digests.
- `internal/anonymous/storage_postgres_test.go` — Tests des requêtes et erreurs du repository de sessions anonymes.
- `internal/config/config.go` — Chargement et validation centralisés de la configuration applicative.
- `internal/config/config_test.go` — Tests des valeurs par défaut et des invariants de configuration.
- `internal/job/job.go` — Domaine, ownership, snapshot d’identité et orchestration des opérations durables sur les jobs.
- `internal/job/job_test.go` — Tests des politiques de création, d’identité et de délégation du domaine job.
- `internal/job/queue.go` — Politique worker des claims, leases, transitions et reprises bornées de jobs.
- `internal/job/queue_test.go` — Tests des durées, identités worker et validations de transitions de la queue.
- `internal/job/status.go` — Machine d’états et transitions autorisées des jobs PDF.
- `internal/job/status_test.go` — Tests exhaustifs des transitions et états terminaux des jobs.
- `internal/job/storage_postgres.go` — Repository pgx propriétaire des jobs, annulations et tombstones idempotents.
- `internal/job/storage_postgres_integration_test.go` — Test PostgreSQL opt-in du cycle propriétaire complet d’un job.
- `internal/job/storage_postgres_queue.go` — Claims SKIP LOCKED, heartbeats, transitions sous lease et reprise PostgreSQL.
- `internal/job/storage_postgres_queue_integration_test.go` — Test PostgreSQL opt-in avec workers distincts, lease perdu et retries épuisés.
- `internal/job/storage_postgres_queue_test.go` — Tests SQL des prédicats de claim, heartbeat, transition et récupération.
- `internal/job/storage_postgres_test.go` — Tests des requêtes, contrôles d’ownership et mutations idempotentes des jobs.
- `internal/smtppoc/client.go` — Construction et envoi direct de messages SMTP selon le mode TLS choisi.
- `internal/smtppoc/client_test.go` — Tests sans réseau de validation et de composition SMTP.
- `internal/upload/storage_postgres.go` — Repository pgx des sessions et parties d’upload avec verrouillage transactionnel.
- `internal/upload/storage_postgres_integration_test.go` — Test PostgreSQL opt-in du cycle persistant complet d’un upload.
- `internal/upload/storage_postgres_test.go` — Tests des invariants transactionnels, de propriété et d’idempotence des uploads.
- `internal/upload/upload.go` — Domaine des uploads découpés, propriétaires, digests et transitions persistantes.
- `internal/upload/upload_test.go` — Tests de validation, sanitation et construction des sessions d’upload.
- `internal/version/version.go` — Source de vérité de la version et identité des builds.
- `internal/version/version_test.go` — Tests du rendu de l’identité de version.

## Documentation

- `docs/DOCUMENTATION.md` — Contrat normatif et réutilisable de qualité documentaire pour humains, agents et releases.
- `docs/ROADMAP.md` — Source de vérité de l’ordre, du statut et des critères des tâches GPF.
- `docs/atlas.md` — Inventaire exact fichier par fichier du dépôt.
- `docs/brief_go_pdf_self_service_agent.docx` — Brief produit original conservé dans son format bureautique source.
- `docs/brief_go_pdf_self_service_agent.md` — Transcription Markdown exploitable du brief produit.

## Scripts opératoires

- `scripts/changelog_section.sh` — Extraction d’une section versionnée du changelog pour la publication.
- `scripts/check_documentation_claims.sh` — Assertions reliant les promesses documentaires critiques au code et aux exemples.
- `scripts/check_release_traceability.sh` — Contrôle bidirectionnel entre tâches roadmap terminées et changelog versionné.
- `scripts/createLocalDBAndUser.sh` — Création sûre de la base locale, de son rôle et du fichier d’environnement privé.
- `scripts/reducePdfSize.sh` — Référence historique du traitement Ghostscript manuel à remplacer par le worker.
- `scripts/release.sh` — Garde finale, création du tag annoté et push atomique de la release.
