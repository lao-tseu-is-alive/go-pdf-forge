# Changelog

Toutes les modifications notables de `go-pdf-forge` sont documentées ici.
Le format suit [Keep a Changelog](https://keepachangelog.com/fr/1.1.0/) et le
projet utilise le versionnement sémantique.

## [Unreleased]

### À venir

- Sessions anonymes persistantes avec capacités révocables.
- Repository PostgreSQL avec gestion des leases et reprise des jobs.
- Stockage multipart Garage/S3 et handlers ConnectRPC.
- Worker Ghostscript et interface Vue.

## [0.0.3] - 2026-09-17

Cette version ancre une politique documentaire vérifiable dans le cycle normal
de développement et de release.

### Added

- **GPF-035** — Contrôle GoDoc déterministe des packages et API exportées,
  lint des commentaires Protobuf, atlas bidirectionnel par chemin exact et
  assertions reliant les promesses documentaires critiques au code.
- Politique de documentation expliquant les contrats attendus, les exclusions
  et le workflow contributeur.

### Changed

- `make check`, la CI et `make release-check` exécutent désormais la garde
  documentaire complète.
- Le contrôle de reproductibilité compare désormais les bindings générés avant
  et après Buf, sans rejeter les changements générés légitimes non commités.
- La garde de release vérifie la traçabilité bidirectionnelle entre les tâches
  terminées de la roadmap et les sections versionnées du changelog.

## [0.0.2] - 2026-09-16

Cette version ajoute le socle PostgreSQL opérationnel et le suivi versionné de
la feuille de route.

### Added

- Roadmap d'implémentation versionnée avec identifiants de tâches stables,
  critères de sortie par phase et contrôle automatique avant release.
- **GPF-001** — Socle PostgreSQL `pgxpool` avec limites et timeouts
  configurables, logs sans DSN ni mot de passe, health check borné et
  migrations embarquées explicites.

## [0.0.1] - 2026-09-15

Première fondation pré-alpha. Cette version établit les contrats et les
invariants du service ; elle ne fournit pas encore le parcours PDF complet.

### Added

- Architecture API/worker avec PostgreSQL comme file durable et Garage/S3 pour
  les objets temporaires.
- Contrats Protobuf/ConnectRPC pour les sessions anonymes, uploads découpés et
  jobs PDF, avec bindings Go et TypeScript générés.
- Capacités anonymes de 256 bits dont seul un digest HMAC-SHA-256 est conservé.
- Configuration validée pour PostgreSQL, Garage, limites d'upload et traitement.
- Migration initiale pour les sessions, quotas, uploads, jobs, leases et outbox.
- Cycle d'état des jobs et tests unitaires.
- Diagnostic SMTP `mail-poc` avec relais en clair, STARTTLS, TLS implicite et
  authentification optionnelle, validé par la remise d'un message via le relais
  interne sans authentification.
- Script de création de la base locale générant un mot de passe aléatoire sans
  l'exposer dans les arguments des processus et écrivant un `.env` privé.
- Identité de build centralisée avec version, commit Git et date de compilation.
- Garde de release vérifiant la version, le README, le changelog, les tests,
  `go vet`, les contrats Protobuf et la reproductibilité de la génération.

[Unreleased]: https://github.com/lao-tseu-is-alive/go-pdf-forge/compare/v0.0.3...HEAD
[0.0.3]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.3
[0.0.2]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.2
[0.0.1]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.1
