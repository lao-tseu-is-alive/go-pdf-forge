# PDF Self-Service — Cahier des charges pour agent

PDF SELF-SERVICE

# Cahier des charges technique
et prompt d’implémentation pour agent

Service interne de traitement asynchrone de PDF en Go
Réduction adaptative, upload robuste, suivi temps réel, notification email et base pour de futurs traitements PDF

Destinataire : agent de développement autonome (Claude Code, Codex/Cortex ou équivalent)
Date : 15 septembre 2026

## 1. Mission du projet

Construire un petit service cloud-native, déployable sur le cluster k3s interne, permettant à un collaborateur de déposer un PDF trop volumineux, de suivre son traitement et de récupérer automatiquement une version optimisée. Le service doit également constituer une base propre pour ajouter ultérieurement d’autres opérations PDF.

### 1.1 Expérience utilisateur cible

- L’utilisateur interne arrive sur une interface web simple et est identifié avec le mécanisme JWT déjà utilisé dans les applications internes.
- Il choisit un PDF local et lance l’upload; la progression de l’upload est visible.
- Le serveur crée un job asynchrone. Le navigateur suit son évolution en temps réel via un flux SSE consommé avec fetch() et Authorization: Bearer <JWT>.
- Le service analyse le PDF puis tente une réduction de qualité adaptative en conservant la meilleure qualité possible sous la limite configurée.
- Si l’onglet reste ouvert, l’utilisateur voit la progression jusqu’au téléchargement.
- Si l’utilisateur ferme la page, le traitement continue. Si la notification est activée, un email est envoyé à l’adresse issue de l’identité JWT quand le résultat est prêt.
- Les fichiers sont temporaires et automatiquement supprimés après une durée de rétention configurable.
### 1.2 Non-objectifs du MVP

- Ne pas réécrire un moteur PDF en Go.
- Ne pas remplacer Ghostscript par une bibliothèque Go « pour faire plus Go ».
- Ne pas introduire NATS, Kafka, Temporal ou Redis tant que PostgreSQL suffit pour la file de jobs.
- Ne pas implémenter de mode anonyme/public dans le MVP.
- Ne pas dépendre de cookies HttpOnly/Secure. Le contexte d’infrastructure impose le JWT Bearer stocké côté session navigateur.
- Ne pas transformer ce service en GED permanente.
## 2. Références existantes à réutiliser

Sources GitHub : https://github.com/lao-tseu-is-alive/go-grpc-file-upload ; https://github.com/lao-tseu-is-alive/go-cloud-k8s-employe-jwt ; https://github.com/lao-tseu-is-alive/go-geo-tree-table

## 3. Architecture cible du MVP

```text
Browser (Vue/TS)
   | JWT Bearer
   | StartUpload / UploadChunk / CommitUpload
   v
+----------------------+        +-------------------+
| pdf-service API Go   |------->| PostgreSQL        |
| ConnectRPC + HTTP    |        | jobs + outbox     |
| SSE via fetch()      |        +---------+---------+
+----------+-----------+                  | claim job
           | temp files / BlobStore       v
           |                     +-------------------+
           +-------------------->| pdf-worker Go     |
                                 | pdfinfo/pdfimages  |
                                 | Ghostscript        |
                                 +---------+---------+
                                           | completed/failed
                                           v
                                      outbox email
                                           |
                                           v
                                      SMTP interne
```

### 3.1 Déployables

### 3.2 Choix de queue

Utiliser PostgreSQL comme source de vérité et file de travail. Les workers réclament les jobs avec SELECT … FOR UPDATE SKIP LOCKED. Cela suffit au besoin attendu et évite une dépendance de messagerie supplémentaire. Le modèle peut évoluer plus tard vers JetStream sans changer le contrat métier.

```text
SELECT id FROM pdf_job WHERE status='queued' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1;
```

## 4. Identité, authentification et ownership

Le service PDF ne doit jamais interpréter directement les headers F5. Cette responsabilité reste dans go-cloud-k8s-employe-jwt. Le nouveau service ne fait confiance qu’au JWT signé et validé par le middleware partagé.

- Claims utiles : user_id, external_id, login, name, email, is_admin, exp.
- À la création du job, extraire l’identité et la persister comme snapshot métier. Ne jamais placer le JWT dans la queue.
- Un utilisateur non-admin ne peut lister, suivre, télécharger, annuler ou supprimer que ses propres jobs.
- L’adresse de notification interne vient du claim email et n’est pas éditable dans le MVP.
- Le JWT reste en sessionStorage conformément aux contraintes d’infrastructure existantes. Ne pas proposer ni réintroduire de cookie HttpOnly/Secure.
```text
type Principal struct {
    UserID     int64
    ExternalID string
    Login      string
    Name       string
    Email      string
    IsAdmin    bool
}
```

## 5. Upload navigateur : évolution de go-grpc-file-upload

Conserver l’idée centrale du dépôt existant : protocole typé, hash SHA-256, sanitation du nom, annulation par context et commit final. En revanche, pour le navigateur, remplacer le RPC unary contenant tout le fichier par un protocole chunké.

### 5.1 Contrat recommandé

```text
service UploadService {
  rpc StartUpload(StartUploadRequest) returns (StartUploadResponse);
  rpc UploadChunk(UploadChunkRequest) returns (UploadChunkResponse);
  rpc CommitUpload(CommitUploadRequest) returns (CommitUploadResponse);
  rpc AbortUpload(AbortUploadRequest) returns (AbortUploadResponse);
}

message StartUploadRequest {
  string filename = 1;
  int64 size = 2;
  string content_type = 3;
}
message StartUploadResponse { string upload_id = 1; int32 chunk_size = 2; }
message UploadChunkRequest { string upload_id = 1; int32 index = 2; bytes data = 3; }
message CommitUploadRequest { string upload_id = 1; string sha256 = 2; bool notify_email = 3; }
```

### 5.2 Contraintes

- Chunk recommandé initialement : 2 à 8 MiB, configurable.
- Interdire les trous/duplications incohérentes; rendre une retransmission idempotente si même index + même hash.
- Calculer SHA-256 côté navigateur pendant/à la fin de la lecture et côté serveur pendant l’assemblage. Vérifier au commit.
- Ne jamais charger le PDF complet en RAM côté API.
- Limiter la taille maximale d’upload via configuration (ex. 1 GiB au départ, à décider en exploitation).
- Nettoyer automatiquement les uploads incomplets abandonnés après TTL.
## 6. Modèle de job et machine d’états

### 6.1 Schéma PostgreSQL minimal

```text
pdf_job(
  id uuid primary key,
  status text not null,
  created_by_user_id bigint not null,
  created_by_login text not null,
  created_by_name text not null,
  notification_email text,
  notify_email boolean not null default false,
  original_filename text not null,
  input_key text not null,
  output_key text,
  input_size bigint, output_size bigint, page_count int,
  selected_profile text, progress_percent int, progress_message text,
  attempt_count int not null default 0,
  error_code text, error_message text,
  created_at timestamptz not null, started_at timestamptz,
  completed_at timestamptz, expires_at timestamptz not null
);

outbox_event(id uuid, kind text, payload jsonb, created_at timestamptz, processed_at timestamptz, attempts int);
```

## 7. Pipeline PDF et stratégie de réduction

Ghostscript reste le moteur. Le worker l’invoque avec exec.CommandContext, arguments séparés, timeout et ressources limitées. Aucun shell et aucune concaténation de commande avec un nom de fichier utilisateur.

1. Valider que le fichier existe, n’est pas vide et commence par une structure PDF plausible; puis exécuter pdfinfo.
1. Refuser ou signaler explicitement les PDF chiffrés/non traitables selon les capacités choisies.
1. Exécuter pdfimages -list pour télémétrie/diagnostic; ne pas faire dépendre le succès de sa sortie cosmétique.
1. Si le fichier est déjà sous la limite cible, permettre de terminer sans recompression (option configurable).
1. Essayer d’abord la meilleure qualité : profil ~150 DPI /ebook ou paramètres explicites équivalents.
1. Si le résultat est sous la limite, le conserver et arrêter : ne pas générer automatiquement une seconde version.
1. Sinon descendre progressivement (ex. 120 → 100/96 → 72 DPI) jusqu’à la limite ou jusqu’au plancher configuré.
1. Valider la sortie : PDF lisible par pdfinfo, nombre de pages identique, taille > 0, et taille effectivement réduite.
1. Calculer SHA-256 et statistiques de réduction; rendre le résultat disponible.
## 8. Progression et SSE avec Authorization Bearer

Ne pas utiliser EventSource natif, car il n’offre pas le contrôle d’en-tête Authorization requis ici. Utiliser fetch() et lire le ReadableStream SSE.

```text
const response = await fetch(`/api/v1/jobs/${jobId}/events`, {
  headers: { Authorization: `Bearer ${token}` },
  signal: abortController.signal,
});

const reader = response.body!.getReader();
const decoder = new TextDecoder();
// parser les blocs "event:" / "data:" séparés par \n\n
```

- Content-Type: text/event-stream; Cache-Control: no-cache.
- Événements : snapshot, progress, completed, failed, heartbeat.
- Heartbeat toutes les ~15-30 s pour éviter les timeouts intermédiaires.
- Reconnexion : le frontend peut retenter GET events et recevoir immédiatement un snapshot courant.
- Ne pas faire dépendre le job de la connexion SSE. Fermer l’onglet ne doit jamais annuler le traitement.
### 8.1 Progression Ghostscript

Le worker peut parser prudemment les lignes « Page N » de stdout/stderr pour fournir une progression indicative N / page_count. Cette progression est UX, pas une garantie métier. Si le format de sortie change, le traitement ne doit pas échouer.

## 9. Notification email : POC obligatoire avant intégration

Avant de coder l’outbox et le worker de notification, créer cmd/mail-poc. Son seul but est de vérifier qu’un pod/host du même environnement peut envoyer un mail via l’infrastructure SMTP réellement disponible.

```text
go run ./cmd/mail-poc \
  --smtp-host "$SMTP_HOST" \
  --smtp-port "$SMTP_PORT" \
  --from "$SMTP_FROM" \
  --to "adresse.test@..."
```

- Supporter au minimum : SMTP sans auth sur relais interne OU STARTTLS/auth si requis. Ne pas supposer lequel avant le test.
- Timeouts explicites de connexion/commande.
- Objet identifiable : [PDF Service POC] Test SMTP.
- Afficher clairement serveur, mode TLS, destinataire et résultat; ne jamais logger de mot de passe.
- Ajouter ensuite un test d’intégration/configuration documenté utilisable dans k3s.
### 9.1 Notification production

À la transaction qui marque un job completed/failed, insérer un événement outbox. Un composant de notification traite l’outbox avec retries et backoff. L’échec SMTP ne doit jamais repasser un job PDF completed en failed.

- Email de succès : nom du fichier, réduction obtenue, date d’expiration et lien vers l’application/job.
- Ne pas joindre le PDF au mail.
- Le lien exige toujours l’authentification JWT interne; l’email n’est pas une capability secrète.
- Marquer l’événement outbox comme traité après succès SMTP.
## 10. Stockage, rétention et sécurité

Introduire une interface BlobStore afin de ne pas coupler le métier au type de volume disponible dans k3s.

```text
type BlobStore interface {
    Create(ctx context.Context, key string) (io.WriteCloser, error)
    Open(ctx context.Context, key string) (io.ReadCloser, error)
    Stat(ctx context.Context, key string) (BlobInfo, error)
    Delete(ctx context.Context, key string) error
}
```

- Premier backend possible : filesystem partagé/PVC si une classe RWX fiable existe. Sinon implémenter un backend S3-compatible; ne pas imposer MinIO sans besoin.
- Clés internes opaques basées sur UUID, jamais directement le filename utilisateur.
- Nom original uniquement comme métadonnée et Content-Disposition à la restitution.
- Rétention configurable, 24 à 48 h recommandées pour le MVP.
- Cron/job interne de purge idempotent : input, output, chunks orphelins et metadata expirées selon politique.
- Worker Ghostscript non-root, root filesystem read-only si possible, workdir temporaire, aucune credential inutile, egress réseau désactivé si compatible avec le cluster.
- Requests/limits CPU + mémoire + ephemeral-storage; timeout Ghostscript configurable.
- Limiter le nombre de jobs Ghostscript concurrents indépendamment du nombre de requêtes API.
- Valider MIME/extension sans leur faire confiance comme preuve de sécurité; traiter le PDF comme contenu non fiable.
## 11. API métier et interface web

### 11.1 Écran MVP

```text
Réduire un PDF
[ Choisir un fichier ]   rapport.pdf — 208 Mo
[✓] Me prévenir par email quand le fichier est prêt
    carlos...@... (issu de votre identité)

[ Envoyer et optimiser ]

Upload       ███████████████████ 100 %
Analyse      ✓ 51 pages
Optimisation ██████████████░░░░░ 73 %  (page 37/51)
Vérification ...

Résultat : 208 Mo → 47 Mo — profil 150 DPI
[ Télécharger ]
```

## 12. Observabilité et robustesse

- Logs structurés avec job_id, user_id, login, phase, durée; ne pas logger le JWT ni le contenu.
- Métriques : jobs queued/running/completed/failed, durée par phase, bytes in/out, ratio de réduction, queue age, emails success/failure, purge count.
- Readiness de l’API indépendante de Ghostscript; readiness du worker vérifie DB et présence/version de gs/pdfinfo.
- Graceful shutdown : API termine/stoppe proprement les uploads; worker ne prend plus de job et termine ou marque recoverable selon timeout.
- Au démarrage, récupérer les jobs stuck en analyzing/optimizing dont le lease est expiré.
### 12.1 Tests indispensables

## 13. Structure de dépôt recommandée

```text
go-pdf-self-service/
├── api/                         # .proto / Buf
│   ├── upload/v1/
│   └── pdfjob/v1/
├── gen/                         # Go/TS généré
├── cmd/
│   ├── pdf-api/
│   ├── pdf-worker/
│   └── mail-poc/
├── internal/
│   ├── auth/                    # Principal depuis JWT partagé
│   ├── upload/                  # sessions/chunks/commit
│   ├── jobs/                    # domaine + store PostgreSQL
│   ├── pdf/                     # analyse + Ghostscript runner
│   ├── blob/                    # BlobStore + filesystem/S3 optionnel
│   ├── notify/                  # SMTP + outbox
│   └── observability/
├── migrations/
├── web/                         # Vue 3 + TypeScript
├── deploy/k8s/                  # manifests/Helm/Kustomize selon habitudes repo
├── Dockerfile.api
├── Dockerfile.worker
├── buf.yaml / buf.gen.yaml
└── README.md
```

Le nom définitif du dépôt peut être différent. L’agent ne doit pas créer artificiellement une multitude de microservices : API et worker sont deux processus déployables du même domaine et du même dépôt pour le MVP.

## 14. Ordre d’implémentation imposé

1. Créer/forker le nouveau dépôt à partir des patterns de go-grpc-file-upload. Mettre à jour module/package names proprement.
1. Écrire cmd/mail-poc et documenter le résultat attendu. Ne pas intégrer les emails avant que ce POC soit validé dans l’environnement réel.
1. Définir les proto Upload et PdfJob, générer Go + TypeScript avec Buf.
1. Implémenter Principal/JWT en réutilisant les bibliothèques/patterns existants; tests d’ownership.
1. Implémenter l’upload chunké avec hash SHA-256, taille max et nettoyage.
1. Créer PostgreSQL migrations + JobStore + claim SKIP LOCKED + lease/recovery.
1. Implémenter pdf-worker : pdfinfo, Ghostscript, stratégie adaptative, validation, progression.
1. Implémenter StreamJobEvents via fetch-SSE + Bearer et reconnexion.
1. Implémenter téléchargement, liste « Mes traitements », annulation et purge TTL.
1. Intégrer outbox + SMTP seulement après validation du mail-poc.
1. Ajouter manifests k3s, resource limits, probes, securityContext et documentation opérationnelle.
1. Tester avec un vrai PDF volumineux comparable au cas 208 MiB / 51 pages.
## 15. Critères d’acceptation du MVP

- Un utilisateur interne authentifié peut uploader depuis un navigateur un PDF > 75 MiB sans que le serveur charge le fichier complet en RAM.
- Un upload corrompu ou dont le SHA-256 ne correspond pas est rejeté et nettoyé.
- Après commit réussi, le navigateur reçoit un job_id et le job survit à la fermeture de la page.
- Deux workers peuvent tourner sans traiter le même job.
- Le worker limite sa concurrence et Ghostscript est borné par timeout/ressources.
- Le service choisit la meilleure qualité qui respecte la limite; il ne génère pas deux sorties sans raison.
- Le frontend suit le traitement avec fetch-SSE + Authorization Bearer et peut se reconnecter.
- Un utilisateur ne peut jamais lire/télécharger le job d’un autre utilisateur.
- L’email, quand activé, est envoyé à l’adresse du JWT via outbox; une panne SMTP ne détruit pas le résultat PDF.
- Les fichiers expirent et sont effectivement purgés.
- Le projet compile, go test ./... passe, buf lint passe, frontend build/test passe, et les images conteneur s’exécutent non-root.
- README contient un quick start local, configuration, architecture, exemples curl/Connect, et procédure de déploiement k3s.
## 16. Directive à donner à l’agent de développement

Le bloc suivant constitue la consigne de travail. Il peut être fourni tel quel avec accès aux dépôts de référence.

```text
Tu es responsable de l’implémentation d’un service Go cloud-native de self-service PDF.

OBJECTIF
Livrer un MVP fonctionnel, testé et déployable sur k3s permettant à un utilisateur interne authentifié par le JWT existant d’uploader un gros PDF, de lancer une réduction Ghostscript asynchrone, de suivre le job via SSE avec Bearer token, de récupérer le résultat et, si demandé, d’être notifié par email.

RÉFÉRENCES À ÉTUDIER AVANT DE CODER
1. https://github.com/lao-tseu-is-alive/go-grpc-file-upload
2. https://github.com/lao-tseu-is-alive/go-cloud-k8s-employe-jwt
3. https://github.com/lao-tseu-is-alive/go-geo-tree-table
4. Le script reducePdfSize.sh fourni avec le brief.

CONTRAINTES NON NÉGOCIABLES
- Go pour backend/worker. Buf + Protobuf + ConnectRPC pour les contrats applicatifs.
- Réutiliser les patterns de go-grpc-file-upload mais NE PAS utiliser son UploadFile unary monolithique pour les gros fichiers navigateur. Implémenter StartUpload/UploadChunk/CommitUpload.
- JWT Bearer en sessionStorage côté frontend; ne pas réintroduire de cookie HttpOnly/Secure.
- SSE consommé avec fetch() afin d’envoyer Authorization: Bearer.
- PostgreSQL comme queue de jobs avec FOR UPDATE SKIP LOCKED; pas de NATS/Redis/Temporal dans le MVP.
- Ghostscript reste le moteur PDF; l’app Go orchestre via exec.CommandContext. Aucun shell.
- Deux binaires principaux : pdf-api et pdf-worker. Ajouter cmd/mail-poc avant l’intégration SMTP.
- Le JWT n’entre jamais dans la queue. Persister un snapshot de l’identité du principal sur le job.
- Les fichiers sont temporaires et purgés par TTL.
- Isolation et limites de ressources fortes autour de Ghostscript.
- Une panne SMTP ne doit jamais faire échouer rétroactivement un job PDF terminé.

MODE DE TRAVAIL
Commence par lire les dépôts et produire un court ARCHITECTURE.md précisant ce que tu réutilises réellement et ce que tu modifies. Ensuite implémente par incréments compilables, avec tests à chaque étape. N’invente pas de nouvelle infrastructure sans justification mesurable. Préfère le code simple, explicite et idiomatique.

LIVRABLES
- Code source complet.
- Proto + génération Buf Go/TypeScript.
- Migrations PostgreSQL.
- Frontend Vue/TypeScript minimal.
- cmd/mail-poc.
- Dockerfiles et manifests k3s.
- Tests unitaires/intégration essentiels.
- README opérationnel et ARCHITECTURE.md.
- Exemple de test d’un gros PDF et preuve qu’un onglet fermé n’annule pas le job.

À CHAQUE DÉCISION IMPORTANTE
Explique brièvement le compromis. Si le brief et le code existant semblent contradictoires, conserve l’objectif métier et signale la divergence au lieu de la masquer.
```

## 17. Décisions laissées ouvertes volontairement


## Tables / décisions complémentaires

| Dépôt | Rôle dans le nouveau projet | Décision |
| --- | --- | --- |
| lao-tseu-is-alive/go-grpc-file-upload | Socle Buf + Protobuf + ConnectRPC; streaming natif, SHA-256, sanitation filename, annulation | Réutiliser/factoriser. Faire évoluer le navigateur vers des chunks. |
| lao-tseu-is-alive/go-cloud-k8s-employe-jwt | Pont F5 → identité applicative → JWT signé | Réutiliser tel quel comme fournisseur d’identité. |
| lao-tseu-is-alive/go-geo-tree-table | Exemple réel de consommation des claims JWT et frontend Vue/TypeScript | Réutiliser les patterns JWT et sessionStorage utiles. |
| reducePdfSize.sh | Heuristique historique : pdfinfo, pdfimages, Ghostscript screen/ebook | Conserver l’intention, moderniser en stratégie adaptative. |

| Binaire | Responsabilité | Scalabilité |
| --- | --- | --- |
| cmd/pdf-api | Authentification JWT, upload, jobs, téléchargement, SSE, ownership | Horizontalement scalable si stockage partagé/objet. |
| cmd/pdf-worker | Claim des jobs, analyse et Ghostscript, validation des sorties | Concurrence volontairement limitée; CPU-bound. |
| cmd/mail-poc | POC isolé SMTP; prouve configuration TLS/auth/from/to avant intégration | Outil de diagnostic, pas un service permanent. |

| État | Signification | Transition typique |
| --- | --- | --- |
| uploading | Chunks en cours | → uploaded / cancelled / expired |
| queued | Upload validé et job prêt | → analyzing |
| analyzing | pdfinfo/pdfimages et contrôles | → optimizing / failed |
| optimizing | Ghostscript en cours | → validating / failed |
| validating | Contrôles résultat | → completed / retry / failed |
| completed | Résultat disponible | → expired/deleted |
| failed | Erreur terminale explicite | → retry manuel éventuel |
| cancelled | Annulé par utilisateur | terminal |
| expired | Fichiers supprimés par TTL | terminal |

| Opération | But |
| --- | --- |
| StartUpload / UploadChunk / CommitUpload | Upload robuste et création du job après hash OK |
| ListJobs | Historique temporaire des jobs appartenant au principal |
| GetJob | Snapshot complet du job |
| StreamJobEvents | SSE protégé par Bearer |
| CancelJob | Annulation queued; best effort sur running via contexte/cancellation flag |
| DownloadResult | Stream de sortie avec ownership + Content-Disposition |
| DeleteJob | Suppression anticipée des blobs et metadata selon politique |

| Niveau | Cas |
| --- | --- |
| Unitaires | state machine upload, sanitation, ownership, choix de profil, parsing progression, transitions job, outbox |
| Intégration DB | SKIP LOCKED avec 2 workers, retry/lease, outbox atomique |
| Intégration fichiers | chunks, hash incorrect, upload interrompu, filename malveillant, gros fichier sans explosion mémoire |
| Intégration PDF | petit PDF, 200+ MiB, PDF déjà < limite, encrypted/corrompu, Ghostscript timeout |
| HTTP/SSE | Bearer absent/invalide, utilisateur A ne voit pas B, reconnexion SSE, fermeture browser |
| SMTP POC | envoi réel depuis environnement représentatif |

| Sujet | Décision à prendre avec l’environnement réel |
| --- | --- |
| Stockage temporaire | PVC partagé RWX si fiable; sinon S3-compatible. Garder BlobStore abstrait. |
| SMTP | Relais interne, STARTTLS, auth éventuelle : à déterminer par cmd/mail-poc. |
| Limite cible | 75 MiB historique; la rendre configurable, éventuellement marge sous la limite. |
| TTL | 24 h ou 48 h selon usage/politique. |
| Concurrence worker | Commencer à 1 ou 2 par pod selon CPU/RAM mesurés. |
| Profils GS | /ebook puis paliers explicites; valider qualité sur documents réels. |
| Déploiement | Helm/Kustomize/manifests selon conventions du cluster existant. |
