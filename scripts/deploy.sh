#!/usr/bin/env bash
# deploy.sh — Build, push e deploy do miudinho-agent
#
# Uso:
#   ./scripts/deploy.sh [NAMESPACE] [TAG]
#
# Exemplos:
#   ./scripts/deploy.sh                    # namespace=miudinho-agent, tag=git SHA
#   ./scripts/deploy.sh o11y               # namespace=o11y, tag=git SHA
#   ./scripts/deploy.sh o11y v1.2.3        # namespace=o11y, tag=v1.2.3

set -euo pipefail

# ─── Configurações ───────────────────────────────────────────────────────────
REGISTRY="ghcr.io/wellbastos/miudinho-agent"
IMAGE_NAME="miudinho-agent"
IMAGE="${REGISTRY}/${IMAGE_NAME}"
CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)/charts/miudinho-agent"
RELEASE_NAME="miudinho-agent"
NAMESPACE="o11y"
TAG="${2:-$(git rev-parse --short HEAD)}"
DEPLOY_TIMEOUT="120s"

# ─── Cores ───────────────────────────────────────────────────────────────────
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
RED='\033[0;31m'
NC='\033[0m'

log()  { echo -e "${CYAN}[deploy]${NC} $*"; }
ok()   { echo -e "${GREEN}[ok]${NC} $*"; }
warn() { echo -e "${YELLOW}[warn]${NC} $*"; }
err()  { echo -e "${RED}[erro]${NC} $*" >&2; exit 1; }

# ─── Dependências ────────────────────────────────────────────────────────────
for cmd in docker go helm kubectl gcloud; do
  command -v "$cmd" &>/dev/null || err "Comando '$cmd' não encontrado no PATH"
done

log "Iniciando deploy — imagem: ${IMAGE}:${TAG} | namespace: ${NAMESPACE}"

# ─── 1. Atualizar vendor ─────────────────────────────────────────────────────
log "Atualizando vendor..."
cd "$(dirname "$0")/.."
go mod vendor
ok "vendor atualizado"

# ─── 2. Build da imagem ──────────────────────────────────────────────────────
log "Buildando imagem Docker..."
docker build \
  -t "${IMAGE}:${TAG}" \
  -t "${IMAGE}:latest" \
  .
ok "Imagem buildada: ${IMAGE}:${TAG}"

# ─── 3. Autenticar e fazer push ──────────────────────────────────────────────
log "Configurando autenticação no Artifact Registry..."
gcloud auth configure-docker us-docker.pkg.dev --quiet

log "Fazendo push da imagem..."
docker push "${IMAGE}:${TAG}"
docker push "${IMAGE}:latest"
ok "Push concluído — ${IMAGE}:${TAG}"

# ─── 4. Helm upgrade ─────────────────────────────────────────────────────────
log "Aplicando Helm upgrade (${RELEASE_NAME} → ${NAMESPACE})..."
helm upgrade "${RELEASE_NAME}" "${CHART_DIR}" \
  --install \
  --namespace "${NAMESPACE}" \
  --create-namespace \
  --set image.tag="${TAG}" \
  --set image.pullPolicy=Always \
  --wait \
  --timeout "${DEPLOY_TIMEOUT}" \
  --atomic
ok "Helm upgrade concluído"

# ─── 5. Rollout restart (rotate pods) ────────────────────────────────────────
log "Rotacionando pods (rollout restart)..."
kubectl rollout restart deployment/"${RELEASE_NAME}" \
  --namespace "${NAMESPACE}"

log "Aguardando rollout..."
kubectl rollout status deployment/"${RELEASE_NAME}" \
  --namespace "${NAMESPACE}" \
  --timeout="${DEPLOY_TIMEOUT}"
ok "Pods rotacionados com sucesso"

# ─── 6. Resumo ───────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN} Deploy concluído com sucesso!${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "  Imagem  : ${IMAGE}:${TAG}"
echo -e "  Release : ${RELEASE_NAME}"
echo -e "  NS      : ${NAMESPACE}"
echo ""
kubectl get pods -n "${NAMESPACE}" -l "app.kubernetes.io/name=${IMAGE_NAME}"
