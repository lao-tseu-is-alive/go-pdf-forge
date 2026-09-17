# go-pdf-forge Roadmap

Version suivie : **v0.0.4**.

Ce document est la source de vérité pour l'ordre d'implémentation. Les décisions
stables restent dans `AGENTS.md` et `ARCHITECTURE.md`; le détail historique des
versions reste dans `CHANGELOG.md`.

## Conventions

- Les tâches sont exécutées dans l'ordre des phases, sauf dépendance ou risque
  découvert en cours de route.
- `[ ]` signifie à faire, `[x]` terminé et vérifié, `[~]` en cours.
- Chaque tâche possède un identifiant `GPF-NNN` unique et stable.
- Un changement de portée ou d'ordre est documenté ici avant son implémentation.
- Une tâche n'est terminée que lorsque ses tests et ses critères d'acceptation
  sont satisfaits.

## Prochaine action

La prochaine action est la première tâche non cochée de la phase active : les
quotas anonymes atomiques par session et empreinte IP.

## Qualité transverse

- [x] **GPF-035 — Assurance qualité documentaire** : documenter les contrats Go
  et Protobuf, maintenir un atlas exact fichier par fichier et bloquer les
  dérives via `make docs-check`, la CI et la routine de release.

## Phase 1 — Persistance et file durable

- [x] **GPF-001 — Socle PostgreSQL/pgx** : connexion validée, timeouts,
  observabilité non sensible et exécution contrôlée des migrations.
- [x] **GPF-002 — Sessions anonymes** : créer, retrouver, expirer et révoquer
  une session en ne stockant que le digest de sa capacité.
- [ ] **GPF-003 — Quotas anonymes** : compteurs PostgreSQL atomiques par session
  et empreinte IP, avec limites configurables.
- [ ] **GPF-004 — Uploads persistants** : sessions et parties idempotentes,
  continuité des indices, tailles et SHA-256.
- [ ] **GPF-005 — Repository des jobs** : création, consultation propriétaire,
  annulation et suppression idempotente.
- [ ] **GPF-006 — Claim et leases** : `FOR UPDATE SKIP LOCKED`, heartbeat,
  transitions conditionnelles et reprise des leases expirés.
- [ ] **GPF-007 — Tests PostgreSQL** : migrations aller/retour et tests
  d'intégration concurrents sur une base isolée.

Critères de sortie : aucune coordination en mémoire, isolation stricte des
propriétaires et récupération démontrée après interruption d'un worker.

## Phase 2 — Stockage Garage/S3

- [ ] **GPF-008 — Contrat BlobStore** : interface streaming indépendante du
  fournisseur et implémentation mémoire/fichiers pour les tests.
- [ ] **GPF-009 — Multipart Garage** : création, envoi de parties de 8 MiB,
  finalisation, abandon et reprise idempotente.
- [ ] **GPF-010 — Vérification d'objet** : taille et SHA-256 complets vérifiés en
  streaming avant mise en file du job.
- [ ] **GPF-011 — Purge** : suppression idempotente des objets incomplets,
  expirés et orphelins.

Critères de sortie : aucun PDF complet chargé en mémoire et aucun besoin de
filesystem partagé entre API et workers.

## Phase 3 — API identité et upload

- [ ] **GPF-012 — Adaptateur JWT** : HS512, issuer et claims temporels validés,
  `external_id` numérique et aucun déclassement d'un JWT invalide en anonyme.
- [ ] **GPF-013 — Endpoint de session anonyme** : capacité retournée une seule
  fois, expiration et limitation de création par IP fiable.
- [ ] **GPF-014 — Handlers Connect upload** : `StartUpload`, `UploadChunk`,
  `CommitUpload` et `AbortUpload` avec autorisation par propriétaire.
- [ ] **GPF-015 — Limites HTTP publiques** : taille 256 MiB configurable,
  timeouts, débit et confiance conditionnelle des headers proxy.

Critères de sortie : un navigateur authentifié ou anonyme peut créer un job
durable à partir d'un upload repris sans contourner les quotas.

## Phase 4 — Worker PDF

- [ ] **GPF-016 — Exécution isolée des outils** : scratch borné, deadlines,
  sortie capturée et `exec.CommandContext` sans shell.
- [ ] **GPF-017 — Analyse** : plausibilité PDF, `pdfinfo`, `pdfimages`, détection
  des fichiers illisibles ou chiffrés et nombre de pages de référence.
- [ ] **GPF-018 — Optimisation adaptative** : original si déjà sous la cible,
  `/ebook` vers 150 DPI puis `/screen` vers 72 DPI si nécessaire.
- [ ] **GPF-019 — Validation best effort** : lisibilité et nombre de pages,
  conservation du plus petit résultat valide et `target_met` explicite.
- [ ] **GPF-020 — Annulation et reprise** : annulation coopérative, heartbeat,
  retries bornés et simulation d'arrêt brutal.

Critères de sortie : un PDF réel traverse le pipeline, et une cible non atteinte
produit malgré tout le meilleur document valide plutôt qu'un faux échec.

## Phase 5 — Consultation, événements et notifications

- [ ] **GPF-021 — API jobs** : liste, détail, annulation, suppression et
  téléchargement streamé avec contrôle de propriété en base.
- [ ] **GPF-022 — Fetch-SSE** : snapshot autoritaire, progression, heartbeat et
  reconnexion indépendante de la durée du job.
- [ ] **GPF-023 — Outbox SMTP** : notification authentifiée seulement, retry et
  backoff sans dégrader un job PDF terminé.
- [ ] **GPF-024 — Rétention** : expiration par défaut après 48 heures et purge
  cohérente des objets, sessions et métadonnées.

Critères de sortie : les événements fonctionnent avec plusieurs réplicas API et
aucune notification email n'est proposée aux utilisateurs anonymes.

## Phase 6 — Interface Vue

- [ ] **GPF-025 — Fondation frontend** : Vue 3, TypeScript, client Connect et
  configuration runtime.
- [ ] **GPF-026 — Identité** : JWT interne en `sessionStorage` et capacité
  anonyme en `localStorage` jusqu'à expiration.
- [ ] **GPF-027 — Upload reprenable** : découpage client, hash, progression et
  reprise après erreur réseau.
- [ ] **GPF-028 — Suivi des jobs** : liste, fetch-SSE, annulation,
  téléchargement et présentation honnête du best effort.
- [ ] **GPF-029 — Tests navigateur** : parcours authentifié et anonyme, reprise,
  expiration et erreurs accessibles.

## Phase 7 — Livraison et durcissement

- [ ] **GPF-030 — Conteneurs** : images API/worker non-root avec Ghostscript et
  Poppler, filesystem racine en lecture seule et limites de ressources.
- [ ] **GPF-031 — Rancher Desktop** : déploiement local reproductible avec
  PostgreSQL et Garage accessibles depuis k3s.
- [ ] **GPF-032 — Kustomize k3s interne** : namespace, Sealed Secrets,
  `LoadBalancer`, probes, policies et PostgreSQL externe.
- [ ] **GPF-033 — VPS systemd** : unités durcies API/worker et reverse proxy TLS
  avec limitations grossières en complément des quotas applicatifs.
- [ ] **GPF-034 — Observabilité et sécurité** : métriques, logs structurés,
  traces utiles, scan d'image et tests de charge ciblés.

## Décisions encore ouvertes

- Topologie et localisation du Garage de production.
- Valeurs initiales exactes des quotas anonymes publics.
- Fournisseur SMTP authentifié pour le VPS et règles de domaine expéditeur.
- Adresse, ports et terminaison F5 du service interne.
- Objectifs de disponibilité et capacité attendue avant dimensionnement final.

Ces décisions ne bloquent pas les deux premières phases.

## Critères de release

Toute release doit :

1. mettre à jour les tâches et décisions concernées dans ce fichier ;
2. mettre à jour `CHANGELOG.md`, la version Go et le bandeau README ;
3. passer `make release-check` ;
4. produire des artefacts identifiables par version, commit et date ;
5. ne contenir aucun secret ni dépendre d'un fichier local non versionné.
