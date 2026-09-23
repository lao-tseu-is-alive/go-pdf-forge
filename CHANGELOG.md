# Changelog

Toutes les modifications notables de `go-pdf-forge` sont documentées ici.
Le format suit [Keep a Changelog](https://keepachangelog.com/fr/1.1.0/) et le
projet utilise le versionnement sémantique.

## [Unreleased]

### À venir

- Repository PostgreSQL avec gestion des leases et reprise des jobs.
- Stockage multipart Garage/S3 et handlers ConnectRPC.
- Worker Ghostscript et interface Vue.

## [0.0.6] - 2026-09-23

Cette version livre le socle persistant des uploads découpés sans stocker les
octets PDF dans PostgreSQL ni dépendre d'un filesystem local partagé.

### Added

- **GPF-004** — Sessions d'upload propriétaires et parties PostgreSQL
  idempotentes, avec indices strictement contigus, tailles exactes et SHA-256
  canoniques.
- Transitions durables `uploading`, `committing`, `committed`, `aborted` et
  `expired`, préparation de commit vérifiant chaque partie et abandon
  idempotent.
- Sanitation des noms d'affichage, clés objet opaques et contrôle de propriété
  authentifiée ou anonyme directement dans les requêtes SQL.
- Expiration configurable des uploads incomplets, fixée à 24 heures par défaut.
- Migration append-only renforçant les formats SHA-256, la cohérence du commit,
  les timestamps et les bornes des métadonnées backend.
- Test d'intégration PostgreSQL opt-in couvrant le cycle complet, les retries,
  les trous d'indice, l'isolation propriétaire et le nettoyage final.

## [0.0.5] - 2026-09-23

Cette version livre les quotas anonymes persistants et atomiques qui protègent
les futurs endpoints publics sans coordination locale entre réplicas.

### Added

- **GPF-003** — Compteurs PostgreSQL atomiques par session et empreinte IP pour
  les créations de sessions, démarrages d'uploads, créations de jobs et octets
  validés.
- Limites configurables par fenêtre UTC, avec valeurs initiales documentées et
  erreurs de dépassement dépourvues d'identifiants sensibles.
- Migration append-only ajoutant les agrégats par IP et l'index des agrégats
  par session.
- Test d'intégration PostgreSQL opt-in démontrant le respect exact d'une limite
  sous concurrence et le nettoyage des données temporaires.

### Fixed

- La garde de reproductibilité échoue désormais immédiatement si la génération
  Buf ne peut pas s'exécuter, au lieu de comparer silencieusement les anciens
  fichiers générés.

## [0.0.4] - 2026-09-17

Cette version livre le cycle de vie persistant et sécurisé des sessions
anonymes, fondation des futurs quotas et uploads publics.

### Added

- **GPF-002** — Manager de sessions anonymes couvrant création,
  authentification, expiration exclusive et révocation idempotente.
- Repository PostgreSQL `pgx` qui ne reçoit ni capacité brute ni adresse IP
  brute, avec mise à jour conditionnelle empêchant une course avec
  l'expiration ou la révocation.
- Digests HMAC-SHA-256 des adresses IP canoniques avec séparation de domaine.
- Migration append-only imposant des digests de 32 octets, validée sur la base
  locale avec un smoke test transactionnel intégralement annulé.

### Fixed

- Les workflows CI et release installent explicitement `ripgrep`, requis par
  les gardes du dépôt sur les runners GitHub Ubuntu.

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

[Unreleased]: https://github.com/lao-tseu-is-alive/go-pdf-forge/compare/v0.0.6...HEAD
[0.0.6]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.6
[0.0.5]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.5
[0.0.4]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.4
[0.0.3]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.3
[0.0.2]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.2
[0.0.1]: https://github.com/lao-tseu-is-alive/go-pdf-forge/releases/tag/v0.0.1
