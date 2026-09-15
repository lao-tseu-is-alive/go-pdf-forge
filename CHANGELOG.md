# Changelog

Toutes les modifications notables de `go-pdf-forge` sont documentées ici.
Le format suit [Keep a Changelog](https://keepachangelog.com/fr/1.1.0/) et le
projet utilise le versionnement sémantique.

## [Unreleased]

### À venir

- Repository PostgreSQL avec gestion des leases et reprise des jobs.
- Stockage multipart Garage/S3 et handlers ConnectRPC.
- Worker Ghostscript et interface Vue.

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

[Unreleased]: https://github.com/lao-tseu-is-alive/go-pdf-forge/compare/v0.0.1...HEAD
[0.0.1]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.1
