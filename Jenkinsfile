pipeline {
  agent any

  options {
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '20'))
    skipDefaultCheckout(true)
  }

  parameters {
    booleanParam(name: 'DEPLOY_PROD', defaultValue: false, description: '仅手动运行：在 activeSlot=prod-a 时将已验证的 test 三镜像晋级到 prod-B；activeSlot=prod-b 时 fail-closed。')
    booleanParam(name: 'REVERSE_DEPLOY_PROD', defaultValue: false, description: '仅手动运行：明确创建 prod-B stable -> prod-A candidate 的反向蓝绿 release intent；只写候选，不切 owner 或 stable Service。')
    booleanParam(name: 'ROLLBACK_PROD', defaultValue: false, description: '仅手动运行：从已验证的 prod 三镜像历史中选择一个版本回滚。')
  }

  environment {
    // CI 墙上时钟固定为 UTC；业务日历时区由应用显式配置。
    TZ = 'UTC'
    HARBOR_REPOSITORY_NODE = 'platform/juhe-ai'
    HARBOR_REPOSITORY_JOBS = 'platform/juhe-ai-go-jobs'
    HARBOR_REPOSITORY_GATEWAY = 'platform/juhe-ai-go-gateway'
    HARBOR_CACHE_REPOSITORY = 'platform/ci-cache'
    HARBOR_REGISTRY_FILE = '/run/jenkins-secrets/harbor-registry'
    HARBOR_BASE_IMAGES_FILE = '/run/jenkins-secrets/harbor-base-images'
    GITEE_WRITE_KEY = '/run/jenkins-secrets/gitee-k8s-write'
    RELEASE_OBSERVER_KUBECONFIG = '/run/jenkins-secrets/kubeconfig-release-observer'
    PLATFORM_REPOSITORY = 'git@gitee.com:huanminabc/k8s-juhe.git'
    RELEASE_BRANCH = 'main'
    // Jenkins runs on infra-linux.  Keep release verification on the local
    // NodePort path so an app-mac-vm LAN/NAT flap cannot be mistaken for a
    // failed application release.
    INGRESS_ENDPOINT = 'http://127.0.0.1:32080'
    PROMETHEUS_ENDPOINT = 'http://127.0.0.1:19091'
    BUILD_HTTP_PROXY = 'http://10.66.45.2:7890'
    BUILD_NO_PROXY = '127.0.0.1,localhost,192.168.1.76,192.168.1.203,10.66.45.2'
  }

  stages {
    stage('检出源码') {
      steps {
        checkout scm
        script {
          env.SOURCE_COMMIT = sh(script: 'git rev-parse --short=12 HEAD', returnStdout: true).trim()
          env.SOURCE_COMMIT_FULL = sh(script: 'git rev-parse HEAD', returnStdout: true).trim()
          env.SOURCE_SUBJECT = sh(script: 'git log -1 --pretty=%s', returnStdout: true).trim()
          env.HARBOR_REGISTRY = readFile(env.HARBOR_REGISTRY_FILE).trim()
          if (!validRegistry(env.HARBOR_REGISTRY)) {
            error 'HARBOR_REGISTRY 必须是 host 或 host:port。'
          }
          def baseImages = readHarborBaseImages()
          env.NODE_RUNTIME_IMAGE = baseImages.NODE_RUNTIME_IMAGE
          env.NODE_RUNTIME_PNPM_IMAGE = baseImages.NODE_RUNTIME_PNPM_10_32_IMAGE
          env.NODE_BUILDER_PNPM_IMAGE = baseImages.NODE_BUILDER_PNPM_10_32_IMAGE
          env.GO_IMAGE = baseImages.GO_IMAGE
          env.RUNTIME_IMAGE = baseImages.RUNTIME_IMAGE
          if (!fileExists(env.GITEE_WRITE_KEY)) {
            error '缺少平台 GitOps 发布状态写入密钥。'
          }
        }
      }
    }

    stage('检查手动发布参数') {
      when { expression { params.DEPLOY_PROD || reverseDeployRequested() || rollbackRequested() } }
      steps {
        script {
          if ([params.DEPLOY_PROD, reverseDeployRequested(), rollbackRequested()].findAll { it }.size() > 1) {
            error 'DEPLOY_PROD、REVERSE_DEPLOY_PROD 与 ROLLBACK_PROD 只能选择一个。'
          }
        }
      }
    }

    stage('test 发布前置检查') {
      when { expression { !params.DEPLOY_PROD && !reverseDeployRequested() && !rollbackRequested() } }
      steps {
        script {
          preflightTestRelease()
        }
      }
    }

    stage('构建前端与 Node 产物') {
      when { expression { !params.DEPLOY_PROD && !reverseDeployRequested() && !rollbackRequested() } }
      steps {
        withCredentials([usernamePassword(credentialsId: 'harbor-platform-push', usernameVariable: 'HARBOR_USERNAME', passwordVariable: 'HARBOR_PASSWORD')]) {
          sh '''#!/bin/sh
            set -eu
            builder_image="juhe-ai-node-builder:${BUILD_TAG}"
            builder_container="juhe-ai-node-builder-${BUILD_TAG}"
            cache_ref="$HARBOR_REGISTRY/$HARBOR_CACHE_REPOSITORY/juhe-ai-node-builder:buildcache"
            trap 'docker rm -f "$builder_container" >/dev/null 2>&1 || true; docker image rm "$builder_image" >/dev/null 2>&1 || true' EXIT
            printf '%s' "$HARBOR_PASSWORD" | docker login "$HARBOR_REGISTRY" --username "$HARBOR_USERNAME" --password-stdin
            build_with_cache() {
              cache_ref=$1
              shift
              if docker manifest inspect "$cache_ref" >/dev/null 2>&1; then
                docker buildx build --cache-from "type=registry,ref=$cache_ref" --cache-to "type=registry,ref=$cache_ref,mode=max" "$@"
              else
                docker buildx build --cache-to "type=registry,ref=$cache_ref,mode=max" "$@"
              fi
            }
            build_with_cache "$cache_ref" --load --network host \
              --build-arg HTTP_PROXY="$BUILD_HTTP_PROXY" --build-arg HTTPS_PROXY="$BUILD_HTTP_PROXY" --build-arg NO_PROXY="$BUILD_NO_PROXY" \
              --build-arg NODE_BUILDER_PNPM_IMAGE="$NODE_BUILDER_PNPM_IMAGE" \
              --build-arg VITE_JUHE_AI_BUILD_ID="$SOURCE_COMMIT_FULL" \
              --tag "$builder_image" --file docker/Dockerfile.builder .
            docker create --name "$builder_container" "$builder_image" >/dev/null
            mkdir -p backend/dist frontend/dist
            docker cp "$builder_container:/source/backend/dist/." backend/dist/
            docker cp "$builder_container:/source/frontend/dist/." frontend/dist/
          '''
        }
      }
    }

    stage('构建并推送三镜像') {
      when { expression { !params.DEPLOY_PROD && !reverseDeployRequested() && !rollbackRequested() } }
      steps {
        script {
          env.NODE_IMAGE = "${env.HARBOR_REGISTRY}/${env.HARBOR_REPOSITORY_NODE}:${env.SOURCE_COMMIT}"
          env.JOBS_IMAGE = "${env.HARBOR_REGISTRY}/${env.HARBOR_REPOSITORY_JOBS}:${env.SOURCE_COMMIT}"
          env.GATEWAY_IMAGE = "${env.HARBOR_REGISTRY}/${env.HARBOR_REPOSITORY_GATEWAY}:${env.SOURCE_COMMIT}"
        }
        withCredentials([usernamePassword(credentialsId: 'harbor-platform-push', usernameVariable: 'HARBOR_USERNAME', passwordVariable: 'HARBOR_PASSWORD')]) {
          sh '''#!/bin/sh
            set -eu
            printf '%s' "$HARBOR_PASSWORD" | docker login "$HARBOR_REGISTRY" --username "$HARBOR_USERNAME" --password-stdin
            build_with_cache() {
              cache_ref=$1
              shift
              if docker manifest inspect "$cache_ref" >/dev/null 2>&1; then
                docker buildx build --cache-from "type=registry,ref=$cache_ref" --cache-to "type=registry,ref=$cache_ref,mode=max" "$@"
              else
                docker buildx build --cache-to "type=registry,ref=$cache_ref,mode=max" "$@"
              fi
            }
            build_with_cache "$HARBOR_REGISTRY/$HARBOR_CACHE_REPOSITORY/juhe-ai-node-runtime:buildcache" --load --network host \
              --build-arg HTTP_PROXY="$BUILD_HTTP_PROXY" --build-arg HTTPS_PROXY="$BUILD_HTTP_PROXY" --build-arg NO_PROXY="$BUILD_NO_PROXY" \
              --build-arg NODE_RUNTIME_PNPM_IMAGE="$NODE_RUNTIME_PNPM_IMAGE" \
              --tag "$NODE_IMAGE" --file docker/Dockerfile .
            build_with_cache "$HARBOR_REGISTRY/$HARBOR_CACHE_REPOSITORY/juhe-ai-go-jobs:buildcache" --load --network host \
              --build-arg HTTP_PROXY="$BUILD_HTTP_PROXY" --build-arg HTTPS_PROXY="$BUILD_HTTP_PROXY" --build-arg NO_PROXY="$BUILD_NO_PROXY" \
              --build-arg GO_IMAGE="$GO_IMAGE" --build-arg RUNTIME_IMAGE="$RUNTIME_IMAGE" \
              --build-arg GO_PROJECT=jobs --tag "$JOBS_IMAGE" --file docker/Dockerfile.go-project .
            build_with_cache "$HARBOR_REGISTRY/$HARBOR_CACHE_REPOSITORY/juhe-ai-go-gateway:buildcache" --load --network host \
              --build-arg HTTP_PROXY="$BUILD_HTTP_PROXY" --build-arg HTTPS_PROXY="$BUILD_HTTP_PROXY" --build-arg NO_PROXY="$BUILD_NO_PROXY" \
              --build-arg GO_IMAGE="$GO_IMAGE" --build-arg RUNTIME_IMAGE="$RUNTIME_IMAGE" \
              --build-arg GO_PROJECT=gateway --tag "$GATEWAY_IMAGE" --file docker/Dockerfile.go-project .
            docker push "$NODE_IMAGE"
            docker push "$JOBS_IMAGE"
            docker push "$GATEWAY_IMAGE"
            for image in "$NODE_IMAGE" "$JOBS_IMAGE" "$GATEWAY_IMAGE"; do
              digest=$(docker image inspect --format='{{index .RepoDigests 0}}' "$image")
              case "$digest" in *@sha256:*) printf '%s\n' "${digest##*@}" ;; *) echo "无法取得 $image 的不可变 digest" >&2; exit 1 ;; esac
            done > .juhe-ai-digests
          '''
        }
        script {
          def digests = readFile('.juhe-ai-digests').readLines()
          if (digests.size() != 3 || digests.any { !validDigest(it) }) {
            error '三镜像 digest 不完整或格式错误。'
          }
          env.NODE_DIGEST = digests[0]
          env.JOBS_DIGEST = digests[1]
          env.GATEWAY_DIGEST = digests[2]
        }
      }
    }

    stage('写入 test release state') {
      when { expression { !params.DEPLOY_PROD && !reverseDeployRequested() && !rollbackRequested() } }
      steps {
        script {
          env.J3A_MANAGEMENT_ENABLED = sourceUsesDirectJ3aManagement() ? 'true' : 'false'
          env.TEST_RELEASE_STATE_REVISION = writeReleaseState('test', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, env.J3A_MANAGEMENT_ENABLED, 'jenkins-ci')
        }
      }
    }

    stage('验证 test') {
      when { expression { !params.DEPLOY_PROD && !reverseDeployRequested() && !rollbackRequested() } }
      steps {
        script {
          def release = [
            sourceCommit: env.SOURCE_COMMIT,
            nodeDigest: env.NODE_DIGEST,
            jobsDigest: env.JOBS_DIGEST,
            gatewayDigest: env.GATEWAY_DIGEST,
            j3aManagementEnabled: env.J3A_MANAGEMENT_ENABLED
          ]
          waitForArgoApplication('juhe-ai-test', env.TEST_RELEASE_STATE_REVISION)
          waitForIngress('test')
          verifyJ3aRelease('test', release.j3aManagementEnabled)
          markReleaseVerified('test', release.sourceCommit, release.nodeDigest, release.jobsDigest, release.gatewayDigest, release.j3aManagementEnabled)
        }
      }
    }

    stage('读取已验证 test') {
      when { expression { (params.DEPLOY_PROD || reverseDeployRequested()) && !rollbackRequested() } }
      steps {
        script {
          def release = readVerifiedTestRelease()
          env.SOURCE_COMMIT = release.sourceCommit
          env.NODE_DIGEST = release.nodeDigest
          env.JOBS_DIGEST = release.jobsDigest
          env.GATEWAY_DIGEST = release.gatewayDigest
          env.J3A_MANAGEMENT_ENABLED = release.j3aManagementEnabled
        }
      }
    }

    stage('写入 prod 晋级状态') {
      when { expression { params.DEPLOY_PROD && !rollbackRequested() } }
      steps {
        script {
          assertStandardProdPromotionAllowed()
          def release = readVerifiedTestRelease()
          if (release.sourceCommit != env.SOURCE_COMMIT || release.nodeDigest != env.NODE_DIGEST || release.jobsDigest != env.JOBS_DIGEST || release.gatewayDigest != env.GATEWAY_DIGEST || release.j3aManagementEnabled != env.J3A_MANAGEMENT_ENABLED) {
            error 'test release state 在晋级期间发生变化，拒绝写入 prod。'
          }
          env.PROD_RELEASE_STATE_REVISION = writeReleaseState('prod', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, env.J3A_MANAGEMENT_ENABLED, 'jenkins-prod-promotion')
        }
      }
    }

    stage('写入 prod 反向候选状态') {
      when { expression { reverseDeployRequested() } }
      steps {
        script {
          assertReverseProdIntentAllowed()
          def release = readVerifiedTestRelease()
          if (release.sourceCommit != env.SOURCE_COMMIT || release.nodeDigest != env.NODE_DIGEST || release.jobsDigest != env.JOBS_DIGEST || release.gatewayDigest != env.GATEWAY_DIGEST || release.j3aManagementEnabled != env.J3A_MANAGEMENT_ENABLED) {
            error 'test release state 在反向候选写入期间发生变化，拒绝写入 prod candidate。'
          }
          writeReverseReleaseState(env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, env.J3A_MANAGEMENT_ENABLED)
          currentBuild.description = "反向蓝绿候选已写入：prod-b stable -> prod-a candidate，source=${env.SOURCE_COMMIT}；等待 gate/UAT/owner handoff/stable switch"
        }
      }
    }

    stage('选择 prod 回滚版本') {
      when { expression { rollbackRequested() } }
      steps {
        script {
          def rollbackSnapshot = prodRollbackSnapshot()
          def candidates = prodRollbackCandidates()
          if (candidates.isEmpty()) {
            error '没有可回滚的历史 prod 发布。首个 K3s prod 版本已记录；完成下一次验证通过的 prod 晋级后才会出现可选旧版本。'
          }
          def selectedLabel = input(
            message: '选择要恢复的 prod 发布版本。三组件 digest 与 source commit 均从 Git 历史读取，不能手工填写。',
            ok: '开始回滚',
            parameters: [choice(name: 'TARGET_PROD_RELEASE', choices: candidates.keySet().join('\n'), description: '只显示已验证且与当前 prod 不同的历史发布。')]
          )
          if (prodRollbackSnapshot() != rollbackSnapshot) {
            error '等待回滚确认期间，prod release state 或 history 已变化；拒绝回滚。'
          }
          def selected = candidates[selectedLabel]
          if (selected == null) {
            error '所选 prod 发布不存在或已失效。'
          }
          env.SOURCE_COMMIT = selected.sourceCommit
          env.NODE_DIGEST = selected.nodeDigest
          env.JOBS_DIGEST = selected.jobsDigest
          env.GATEWAY_DIGEST = selected.gatewayDigest
          env.J3A_MANAGEMENT_ENABLED = selected.j3aManagementEnabled
          env.PROD_RELEASE_STATE_REVISION = writeReleaseState('prod', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, env.J3A_MANAGEMENT_ENABLED, 'jenkins-prod-rollback')
        }
      }
    }

    stage('验证 prod') {
      when { expression { (params.DEPLOY_PROD && !reverseDeployRequested() && !rollbackRequested()) || rollbackRequested() } }
      steps {
        script {
          waitForArgoApplication('juhe-ai-prod', env.PROD_RELEASE_STATE_REVISION)
          waitForIngress('prod')
          verifyJ3aRelease('prod', env.J3A_MANAGEMENT_ENABLED)
          markReleaseVerified('prod', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, env.J3A_MANAGEMENT_ENABLED)
        }
      }
    }
  }
}

def validRegistry(value) { return value ==~ /^[A-Za-z0-9.-]+(?::[0-9]{1,5})?$/ }
def validDigest(value) { return value ==~ /^sha256:[a-f0-9]{64}$/ }
def validHarborDigestImage(value) {
  if (!value || value != value.trim()) return false
  def prefix = "${env.HARBOR_REGISTRY}/"
  def separator = value.lastIndexOf('@sha256:')
  return value.startsWith(prefix) && separator > prefix.length() &&
    value.indexOf('@sha256:') == separator && validDigest(value.substring(separator + 1))
}
def validCommit(value) { return value ==~ /^[a-f0-9]{7,40}$/ }
def rollbackRequested() { return params.ROLLBACK_PROD }
def reverseDeployRequested() { return params.REVERSE_DEPLOY_PROD }

def assertStandardProdPromotionAllowed() {
  refreshPlatformReleaseWorkspace()
  def activeSlot = metadataValue('prod', 'activeSlot')
  def candidateSlot = metadataValue('prod', 'candidateSlot')
  if (activeSlot != 'prod-a' || candidateSlot != 'prod-b') {
    error "普通 DEPLOY_PROD 只允许 A stable -> B candidate；当前 activeSlot=${activeSlot}, candidateSlot=${candidateSlot}。请使用明确的 REVERSE_DEPLOY_PROD 创建 B stable -> A candidate intent，禁止误入旧单槽位路径。"
  }
}

def assertReverseProdIntentAllowed() {
  refreshPlatformReleaseWorkspace()
  def activeSlot = metadataValue('prod', 'activeSlot')
  def candidateSlot = metadataValue('prod', 'candidateSlot')
  def candidateGate = metadataValue('prod', 'candidateGate')
  if (activeSlot != 'prod-b' || candidateSlot != 'prod-a' || candidateGate != 'blocked') {
    error "反向发布 intent 只允许当前 B stable -> A candidate 且 candidateGate=blocked；实际 activeSlot=${activeSlot}, candidateSlot=${candidateSlot}, candidateGate=${candidateGate}。"
  }
}

def readHarborBaseImages() {
  if (!fileExists(env.HARBOR_BASE_IMAGES_FILE)) {
    error '缺少 Harbor 基础镜像清单；CI 禁止从外网拉取基础镜像。请先运行 platform/harbor/sync-base-images.sh。'
  }
  def requiredKeys = ['NODE_RUNTIME_PNPM_10_32_IMAGE', 'NODE_BUILDER_PNPM_10_32_IMAGE', 'GO_IMAGE', 'RUNTIME_IMAGE']
  def values = [:]
  readFile(env.HARBOR_BASE_IMAGES_FILE).readLines().eachWithIndex { line, index ->
    def trimmed = line.trim()
    if (!trimmed || trimmed.startsWith('#')) {
      return
    }
    def fields = trimmed.split('=', 2)
    if (fields.size() != 2 || !fields[0] || !fields[1] || values.containsKey(fields[0])) {
      error "Harbor 基础镜像清单第 ${index + 1} 行格式错误。"
    }
    values[fields[0]] = fields[1]
  }
  requiredKeys.each { key ->
    def image = values[key]
    if (!validHarborDigestImage(image)) {
      error "Harbor 基础镜像清单中的 ${key} 必须是当前 Harbor 的不可变 digest 引用。"
    }
  }
  return values
}

def releaseWorkspace() { return "${env.WORKSPACE}/.platform-release" }

def prodHistoryPath() { return "${releaseWorkspace()}/apps/juhe-ai/overlays/prod/release-history.tsv" }

def refreshPlatformReleaseWorkspace() {
  // A release operation reads this state more than once to detect races. A
  // transient Gitee Upload Pack disconnect must not turn a safe candidate
  // intent into a false release failure. Each attempt starts with an empty
  // workspace, retains strict host-key verification, and still fails closed
  // after the bounded retry budget is exhausted.
  retry(3) {
    sh "rm -rf '${releaseWorkspace()}' && GIT_SSH_COMMAND=\"ssh -i '${env.GITEE_WRITE_KEY}' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/usr/share/jenkins/ref/gitee-known-hosts\" git clone --depth 1 --branch '${env.RELEASE_BRANCH}' '${env.PLATFORM_REPOSITORY}' '${releaseWorkspace()}'"
  }
}

def metadataValue(environmentName, key) {
  def file = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}/release-metadata.yaml"
  def value = sh(script: "sed -n 's/^  ${key}: \"\\(.*\\)\"/\\1/p' '${file}' | head -n1", returnStdout: true).trim()
  if (!value) error "${environmentName} release state 缺少 ${key}。"
  return value
}

def readTestRelease() {
  refreshPlatformReleaseWorkspace()
  def release = [
    sourceCommit: metadataValue('test', 'sourceCommit'),
    nodeDigest: metadataValue('test', 'nodeImageDigest'),
    jobsDigest: metadataValue('test', 'jobsImageDigest'),
    gatewayDigest: metadataValue('test', 'gatewayImageDigest'),
    j3aManagementEnabled: metadataValue('test', 'j3aManagementEnabled'),
    status: metadataValue('test', 'verification.status'),
    verifiedCommit: metadataValue('test', 'verification.sourceCommit')
  ]
  if (!validCommit(release.sourceCommit) || !validDigest(release.nodeDigest) || !validDigest(release.jobsDigest) || !validDigest(release.gatewayDigest) || !(release.j3aManagementEnabled in ['true', 'false'])) {
    error 'test release state 未通过完整性检查。'
  }
  return release
}

def readVerifiedTestRelease() {
  def release = readTestRelease()
  if (release.status != 'passed' || release.verifiedCommit != release.sourceCommit) {
    error 'test release state 未通过验证门禁。'
  }
  return release
}

def prodRollbackSnapshot() {
  refreshPlatformReleaseWorkspace()
  def metadataFile = "${releaseWorkspace()}/apps/juhe-ai/overlays/prod/release-metadata.yaml"
  def historyFile = prodHistoryPath()
  return [
    gitHead: sh(script: "git -C '${releaseWorkspace()}' rev-parse HEAD", returnStdout: true).trim(),
    metadata: readFile(metadataFile),
    history: fileExists(historyFile) ? readFile(historyFile) : null
  ]
}

def prodRollbackCandidates() {
  def metadataFile = "${releaseWorkspace()}/apps/juhe-ai/overlays/prod/release-metadata.yaml"
  def historyFile = prodHistoryPath()
  if (!fileExists(historyFile)) {
    return [:]
  }
  def current = [
    sourceCommit: metadataValue('prod', 'sourceCommit'),
    nodeDigest: metadataValue('prod', 'nodeImageDigest'),
    jobsDigest: metadataValue('prod', 'jobsImageDigest'),
    gatewayDigest: metadataValue('prod', 'gatewayImageDigest'),
    j3aManagementEnabled: metadataValue('prod', 'j3aManagementEnabled')
  ]
  def candidates = [:]
  readFile(historyFile).readLines().eachWithIndex { line, index ->
    if (!line || line.startsWith('#')) {
      return
    }
    def fields = line.split('\\t', -1)
    if (!(fields.size() in [7, 8])) {
      error "prod release-history.tsv 第 ${index + 1} 行字段数错误。"
    }
    def sourceCommit = fields[2]
    def nodeDigest = fields[3]
    def jobsDigest = fields[4]
    def gatewayDigest = fields[5]
    // Seven-column records predate J3a direct management and are therefore
    // explicit disabled rollback candidates.
    def j3aManagementEnabled = fields.size() == 8 ? fields[6] : 'false'
    def build = fields.size() == 8 ? fields[7] : fields[6]
    if (!(fields[0] ==~ /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/) ||
        !(fields[1] in ['legacy-prod-state', 'jenkins-prod-promotion', 'jenkins-prod-rollback']) ||
        !validCommit(sourceCommit) || !validDigest(nodeDigest) || !validDigest(jobsDigest) || !validDigest(gatewayDigest) || !(j3aManagementEnabled in ['true', 'false']) || !build) {
      error "prod release-history.tsv 第 ${index + 1} 行字段非法。"
    }
    if (sourceCommit == current.sourceCommit && nodeDigest == current.nodeDigest && jobsDigest == current.jobsDigest && gatewayDigest == current.gatewayDigest && j3aManagementEnabled == current.j3aManagementEnabled) {
      return
    }
    def label = "${fields[0]} | ${build} | J3a=${j3aManagementEnabled} | ${sourceCommit} | ${nodeDigest.take(19)} / ${jobsDigest.take(19)} / ${gatewayDigest.take(19)}"
    candidates[label] = [sourceCommit: sourceCommit, nodeDigest: nodeDigest, jobsDigest: jobsDigest, gatewayDigest: gatewayDigest, j3aManagementEnabled: j3aManagementEnabled]
  }
  return candidates
}

def replaceDigest(file, imageName, digest) {
  def expression = 's{(- name: IMAGE\\n\\s+newName: [^\\n]+\\n\\s+digest: )sha256:[a-f0-9]{64}}{$1DIGEST}'
    .replace('IMAGE', imageName)
    .replace('DIGEST', digest)
  sh "perl -0pi -e '${expression}' '${file}'"
}

def sourceUsesDirectJ3aManagement() {
  return fileExists('backend-go/projects/jobs/internal/proxylatency/manual_admin.go') &&
    !fileExists('backend/src/modules/background/proxy-latency-handover.ts') &&
    !fileExists('backend/src/modules/proxies/proxy-test.contract.ts') &&
    !readFile('backend/src/modules/proxies/proxies.routes.ts').contains("proxiesRouter.post('/:id/test'")
}

def configureJ3aManagementRelease(overlay, enabled) {
  if (!(enabled in ['true', 'false'])) error 'J3a 管理 release 状态必须为 true 或 false。'
  def kustomization = "${overlay}/kustomization.yaml"
  if (enabled == 'true') {
    sh "grep -Fqx '  - j3a-management-ingressroute.yaml' '${kustomization}' || sed -i '/^  - ingress.yaml\$/a\\  - j3a-management-ingressroute.yaml' '${kustomization}'"
  } else {
    sh "sed -i '/^  - j3a-management-ingressroute.yaml\$/d' '${kustomization}'"
  }
  sh "sed -i -e 's|^      - JUHE_AI_PROXY_LATENCY_ENABLED=.*|      - JUHE_AI_PROXY_LATENCY_ENABLED=${enabled}|' -e 's|^      - JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED=.*|      - JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED=${enabled}|' '${kustomization}'"
}

def writeReleaseState(environmentName, sourceCommit, nodeDigest, jobsDigest, gatewayDigest, j3aManagementEnabled, actor) {
  if (!validCommit(sourceCommit) || !validDigest(nodeDigest) || !validDigest(jobsDigest) || !validDigest(gatewayDigest) || !(j3aManagementEnabled in ['true', 'false'])) error '发布状态字段不合法。'
  refreshPlatformReleaseWorkspace()
  def overlay = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}"
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai', nodeDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-jobs', jobsDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-gateway', gatewayDigest)
  configureJ3aManagementRelease(overlay, j3aManagementEnabled)
  if (environmentName == 'prod') {
    sh "sed -i 's/replicas: 0/replicas: 1/' '${overlay}/statefulset-patch.yaml'"
  }
  sh """#!/bin/sh
    set -eu
    cd '${releaseWorkspace()}'
    sed -i \\
      -e 's|^  sourceCommit: ".*"|  sourceCommit: "${sourceCommit}"|' \\
      -e 's|^  nodeImageDigest: ".*"|  nodeImageDigest: "${nodeDigest}"|' \\
      -e 's|^  jobsImageDigest: ".*"|  jobsImageDigest: "${jobsDigest}"|' \\
      -e 's|^  gatewayImageDigest: ".*"|  gatewayImageDigest: "${gatewayDigest}"|' \\
      -e 's|^  j3aManagementEnabled: ".*"|  j3aManagementEnabled: "${j3aManagementEnabled}"|' \\
      -e 's|^  releaseActor: ".*"|  releaseActor: "${actor}"|' \\
      -e 's|^  verification.status: ".*"|  verification.status: "pending"|' \\
      -e 's|^  verification.sourceCommit: ".*"|  verification.sourceCommit: ""|' \\
      '${overlay}/release-metadata.yaml'
    git config user.name platform-jenkins
    git config user.email jenkins@jh.huanmin.top
    git add '${overlay}/kustomization.yaml' '${overlay}/release-metadata.yaml' '${overlay}/statefulset-patch.yaml' '${overlay}/j3a-management-ingressroute.yaml'
    if git diff --cached --quiet; then
      echo 'release state 已是目标 source commit 与不可变 digest；继续执行验证，不重复提交。'
    else
      git commit -m '[skip ci] release(juhe-ai-${environmentName}): ${sourceCommit}'
      GIT_SSH_COMMAND="ssh -i '${env.GITEE_WRITE_KEY}' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/usr/share/jenkins/ref/gitee-known-hosts" git push origin HEAD:'${env.RELEASE_BRANCH}'
    fi
  """
  return sh(script: "git -C '${releaseWorkspace()}' rev-parse HEAD", returnStdout: true).trim()
}

def writeReverseReleaseState(sourceCommit, nodeDigest, jobsDigest, gatewayDigest, candidateJ3aManagementEnabled) {
  if (!validCommit(sourceCommit) || !validDigest(nodeDigest) || !validDigest(jobsDigest) || !validDigest(gatewayDigest) || !(candidateJ3aManagementEnabled in ['true', 'false'])) error '反向候选发布状态字段不合法。'
  refreshPlatformReleaseWorkspace()
  def overlay = "${releaseWorkspace()}/apps/juhe-ai/overlays/prod"
  // Top-level fields describe the active slot. In this reverse orientation,
  // B is active; preserve those fields while replacing candidate A fields.
  def activeSourceCommit = metadataValue('prod', 'sourceCommit')
  def activeNodeDigest = metadataValue('prod', 'nodeImageDigest')
  def activeJobsDigest = metadataValue('prod', 'jobsImageDigest')
  def activeGatewayDigest = metadataValue('prod', 'gatewayImageDigest')
  def activeJ3aManagementEnabled = metadataValue('prod', 'j3aManagementEnabled')
  def activeVerificationStatus = metadataValue('prod', 'verification.status')
  def activeVerificationSourceCommit = metadataValue('prod', 'verification.sourceCommit')
  if (!validCommit(activeSourceCommit) || !validDigest(activeNodeDigest) || !validDigest(activeJobsDigest) || !validDigest(activeGatewayDigest) || !(activeJ3aManagementEnabled in ['true', 'false'])) {
    error '当前 prod-B active release state 缺少合法的 source/digest；拒绝创建反向候选。'
  }
  if (!(activeVerificationStatus in ['pending', 'passed']) ||
      (activeVerificationStatus == 'passed' && activeVerificationSourceCommit != activeSourceCommit) ||
      (activeVerificationStatus == 'pending' && activeVerificationSourceCommit)) {
    error '当前 prod-B active verification 状态不合法；拒绝创建反向候选。'
  }
  // In the reverse orientation the primary image names are prod-A. The
  // stable prod-B candidate aliases are intentionally left untouched.
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai', nodeDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-jobs', jobsDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-gateway', gatewayDigest)
  sh """#!/bin/sh
    set -eu
    cd '${releaseWorkspace()}'
    sed -i \\
      -e 's|^  sourceCommit: ".*"|  sourceCommit: "${activeSourceCommit}"|' \\
      -e 's|^  nodeImageDigest: ".*"|  nodeImageDigest: "${activeNodeDigest}"|' \\
      -e 's|^  jobsImageDigest: ".*"|  jobsImageDigest: "${activeJobsDigest}"|' \\
      -e 's|^  gatewayImageDigest: ".*"|  gatewayImageDigest: "${activeGatewayDigest}"|' \\
      -e 's|^  candidateSourceCommit: ".*"|  candidateSourceCommit: "${sourceCommit}"|' \\
      -e 's|^  candidateNodeImageDigest: ".*"|  candidateNodeImageDigest: "${nodeDigest}"|' \\
      -e 's|^  candidateJobsImageDigest: ".*"|  candidateJobsImageDigest: "${jobsDigest}"|' \\
      -e 's|^  candidateGatewayImageDigest: ".*"|  candidateGatewayImageDigest: "${gatewayDigest}"|' \\
      -e 's|^  candidateJ3aManagementEnabled: ".*"|  candidateJ3aManagementEnabled: "${candidateJ3aManagementEnabled}"|' \\
      -e 's|^  activeSlot: ".*"|  activeSlot: "prod-b"|' \\
      -e 's|^  candidateSlot: ".*"|  candidateSlot: "prod-a"|' \\
      -e 's|^  candidateGate: ".*"|  candidateGate: "blocked"|' \\
      -e 's|^  candidateVerification.status: ".*"|  candidateVerification.status: "pending"|' \\
      -e 's|^  candidateVerification.sourceCommit: ".*"|  candidateVerification.sourceCommit: ""|' \\
      -e 's|^  releaseMode: ".*"|  releaseMode: "reverse-blue-green"|' \\
      -e 's|^  releaseActor: ".*"|  releaseActor: "jenkins-prod-reverse-intent"|' \\
      -e 's|^  verification.status: ".*"|  verification.status: "${activeVerificationStatus}"|' \\
      -e 's|^  verification.sourceCommit: ".*"|  verification.sourceCommit: "${activeVerificationSourceCommit}"|' \\
      '${overlay}/release-metadata.yaml'
    git config user.name platform-jenkins
    git config user.email jenkins@jh.huanmin.top
    git add '${overlay}/kustomization.yaml' '${overlay}/release-metadata.yaml'
    if git diff --cached --quiet; then
      echo '反向 prod candidate release intent 已是目标状态；不重复提交。'
    else
      git commit -m '[skip ci] release(juhe-ai-prod): reverse candidate ${sourceCommit}'
      GIT_SSH_COMMAND="ssh -i '${env.GITEE_WRITE_KEY}' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/usr/share/jenkins/ref/gitee-known-hosts" git push origin HEAD:'${env.RELEASE_BRANCH}'
    fi
  """
}

def waitForIngress(environmentName) {
  def host = environmentName == 'test' ? 'test.aijh.huanmin.top' : 'aijh.huanmin.top'
  sh """#!/bin/sh
    set -eu
    i=0
    while [ \$i -lt 60 ]; do
      health=\$(env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY curl --fail --silent --show-error --max-time 10 -H 'Host: ${host}' '${env.INGRESS_ENDPOINT}/__aisys__/api/health' 2>/dev/null || true)
      if printf '%s' "\$health" | grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"'; then exit 0; fi
      i=\$((i + 1)); sleep 5
    done
    echo '${environmentName} 入口、Node DB-ready health 或已启用的 J2/J3a Go-owner readiness 未通过。' >&2
    exit 1
  """
}

def verifyJ3aRelease(environmentName, enabled) {
  if (enabled == 'false') return
  if (enabled != 'true') error "${environmentName} J3a management release 状态非法。"
  def namespace = environmentName == 'test' ? 'juhe-ai-test' : 'juhe-ai-prod'
  def host = environmentName == 'test' ? 'test.aijh.huanmin.top' : 'aijh.huanmin.top'
  def proxyID = env."J3A_${environmentName.toUpperCase()}_MANUAL_PROXY_ID"
  if (!(proxyID ==~ /^[A-Za-z0-9_-]{1,128}$/)) {
    error "${environmentName} J3a 已启用，但 J3A_${environmentName.toUpperCase()}_MANUAL_PROXY_ID 未配置或格式非法。"
  }
  def credentialID = "juhe-j3a-${environmentName}-release-verifier-token"
  withCredentials([string(credentialsId: credentialID, variable: 'J3A_RELEASE_VERIFIER_TOKEN')]) {
    sh """#!/bin/sh
      set -eu
      test -n "\${J3A_RELEASE_VERIFIER_TOKEN:-}" || {
        echo '${environmentName} J3a release verifier token 为空。' >&2
        exit 1
      }
      observer='KUBECONFIG=${env.RELEASE_OBSERVER_KUBECONFIG} kubectl'
      if [ "\$(sh -c "\$observer -n ${namespace} auth can-i get endpoints/juhe-ai")" != 'yes' ]; then
        echo 'J3a release observer 缺少 stable Endpoint 读取权限。' >&2
        exit 1
      fi
      active_pod=\$(KUBECONFIG='${env.RELEASE_OBSERVER_KUBECONFIG}' kubectl -n '${namespace}' get endpoints juhe-ai -o jsonpath='{.subsets[0].addresses[0].targetRef.name}')
      case "\$active_pod" in juhe-ai-0|juhe-ai-b-0) ;; *) echo 'J3a stable Endpoint 未指向允许的 jobs Pod。' >&2; exit 1 ;; esac
      if [ "\$(sh -c "\$observer -n ${namespace} auth can-i get pods/\$active_pod")" != 'yes' ] || \\
         [ "\$(sh -c "\$observer -n ${namespace} auth can-i create pods --subresource=portforward --resource-name=\$active_pod")" != 'yes' ]; then
        echo "J3a release observer 缺少 \$active_pod 的受限 health port-forward 权限。" >&2
        exit 1
      fi
      forward_log=\$(mktemp)
      KUBECONFIG='${env.RELEASE_OBSERVER_KUBECONFIG}' kubectl -n '${namespace}' port-forward "pod/\$active_pod" 33050:3305 >"\$forward_log" 2>&1 &
      forward_pid=\$!
      cleanup() { kill "\$forward_pid" 2>/dev/null || true; wait "\$forward_pid" 2>/dev/null || true; rm -f "\$forward_log"; }
      trap cleanup EXIT HUP INT TERM
      health=''
      i=0
      while [ \$i -lt 20 ]; do
        health=\$(env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY curl --fail --silent --show-error --max-time 3 http://127.0.0.1:33050/health 2>/dev/null || true)
        if [ -n "\$health" ]; then break; fi
        i=\$((i + 1)); sleep 1
      done
      for field in ready proxyLatencyEnabled proxyLatencyReady proxyLatencyOwnerHeld; do
        if ! printf '%s' "\$health" | grep -Eq "\\\"\$field\\\"[[:space:]]*:[[:space:]]*true"; then
          echo "${environmentName} J3a Go health 未满足 \$field=true。" >&2
          exit 1
        fi
      done
      node_health=\$(env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY curl --fail --silent --show-error --max-time 10 -H 'Host: ${host}' '${env.INGRESS_ENDPOINT}/__aisys__/api/health')
      if ! printf '%s' "\$node_health" | grep -Eq '\"proxyLatency\"[[:space:]]*:[[:space:]]*\\{[^}]*\"enabled\"[[:space:]]*:[[:space:]]*false'; then
        echo '${environmentName} active-path-zero 未通过：Node proxyLatency 仍处于 enabled。' >&2
        exit 1
      fi
      started_at=\$(date -u '+%Y-%m-%dT%H:%M:%SZ')
      report=\$(env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY curl --fail --silent --show-error --max-time 30 \\
        -H 'Host: ${host}' \\
        -H "Authorization: Bearer \${J3A_RELEASE_VERIFIER_TOKEN}" \\
        -X POST '${env.INGRESS_ENDPOINT}/__aisys__/api/proxies/${proxyID}/test')
      if ! printf '%s' "\$report" | grep -Eq '"data"[[:space:]]*:'; then
        echo '${environmentName} J3a 精确管理 POST 未返回兼容 report envelope。' >&2
        exit 1
      fi
      i=0
      while [ \$i -lt 12 ]; do
        audit=\$(env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY curl --fail --silent --show-error --max-time 10 \\
          -H 'Host: ${host}' \\
          -H "Authorization: Bearer \${J3A_RELEASE_VERIFIER_TOKEN}" \\
          --get \\
          --data-urlencode 'module=proxies' \\
          --data-urlencode 'action=test' \\
          --data-urlencode 'resourceType=proxy' \\
          --data-urlencode 'resourceId=${proxyID}' \\
          --data-urlencode "startAt=\$started_at" \\
          --data-urlencode 'pageSize=20' \\
          '${env.INGRESS_ENDPOINT}/__aisys__/api/operation-logs' 2>/dev/null || true)
        if printf '%s' "\$audit" | grep -Eq '"operationKey"[[:space:]]*:[[:space:]]*"proxies\\.test"' && \\
           printf '%s' "\$audit" | grep -Eq '"resourceId"[[:space:]]*:[[:space:]]*"${proxyID}"'; then
          exit 0
        fi
        i=\$((i + 1)); sleep 1
      done
      echo '${environmentName} J3a F4 审计管理端读回未在 12 秒内出现。' >&2
      exit 1
    """
  }
}

def preflightTestRelease() {
  sh """#!/bin/sh
    set -eu
    test -r '${env.RELEASE_OBSERVER_KUBECONFIG}' || {
      echo 'Jenkins release observer kubeconfig 不可读。' >&2
      exit 1
    }
    observer='KUBECONFIG=${env.RELEASE_OBSERVER_KUBECONFIG} kubectl'
    for check in \\
      "-n argocd auth can-i get applications.argoproj.io/juhe-ai-test" \\
      "-n juhe-ai-test auth can-i list events" \\
      "-n juhe-ai-test auth can-i get resourcequotas/juhe-ai-test-budget"; do
      if [ "\$(sh -c "\$observer \$check")" != 'yes' ]; then
        echo "release observer 权限不足：\$check" >&2
        exit 1
      fi
    done
    state=\$(KUBECONFIG='${env.RELEASE_OBSERVER_KUBECONFIG}' kubectl -n argocd get application juhe-ai-test -o jsonpath='{.status.sync.status}|{.status.health.status}' 2>&1) || {
      echo "无法读取 test Argo 状态：\$state" >&2
      exit 1
    }
    if [ "\$state" != 'Synced|Healthy' ]; then
      echo "test 当前不是 Synced|Healthy：\$state；拒绝在不稳定基线上构建。" >&2
      exit 1
    fi
    health=\$(env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY curl --fail --silent --show-error --max-time 10 -H 'Host: test.aijh.huanmin.top' '${env.INGRESS_ENDPOINT}/__aisys__/api/health' 2>/dev/null || true)
    if ! printf '%s' "\$health" | grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"'; then
      echo 'test 当前入口或 Node DB-ready/J2/J3a owner readiness 未通过；拒绝开始构建。' >&2
      exit 1
    fi
    prometheus_value() {
      query=\$1
      curl --fail --silent --show-error --max-time 10 --get --data-urlencode "query=\$query" '${env.PROMETHEUS_ENDPOINT}/api/v1/query' |
        sed -n 's/.*"value":\\[[^,]*,"\\([^"\\]*\\)"\\].*/\\1/p' | head -n 1
    }
    blocked=\$(prometheus_value 'sum(pg_blocking_sessions_blocked_sessions{job="postgres",datname="juhe_ai_test"})')
    longest_tx=\$(prometheus_value 'max(pg_stat_activity_max_tx_duration{job="postgres",datname="juhe_ai_test",state=~"active|idle in transaction"})')
    active_alerts=\$(prometheus_value 'sum(ALERTS{namespace="juhe-ai-test",alertstate="firing"}) or vector(0)')
    case "\$blocked" in ''|*[!0-9.eE+-]*) echo "无法读取 test 数据库锁指标：\$blocked" >&2; exit 1 ;; esac
    case "\$longest_tx" in ''|*[!0-9.eE+-]*) echo "无法读取 test 数据库事务指标：\$longest_tx" >&2; exit 1 ;; esac
    case "\$active_alerts" in ''|*[!0-9.eE+-]*) echo "无法读取 juhe-ai-test 告警指标：\$active_alerts" >&2; exit 1 ;; esac
    if awk "BEGIN { exit !(\$blocked > 0 || \$longest_tx > 300) }"; then
      echo "test 数据库存在锁等待或超过 5 分钟的事务（blocked=\$blocked, longestTxSeconds=\$longest_tx）；先清理再发布。" >&2
      exit 1
    fi
    if awk "BEGIN { exit !(\$active_alerts > 0) }"; then
      echo "juhe-ai-test 仍有 firing 告警（count=\$active_alerts）；先恢复节点/Pod 稳定性再发布。" >&2
      exit 1
    fi
  """
}

def waitForArgoApplication(applicationName, expectedRevision) {
  if (!(applicationName ==~ /^juhe-ai-(test|prod)$/)) {
    error "不允许观察未声明的 Argo Application：${applicationName}"
  }
  if (!validCommit(expectedRevision)) {
    error "Argo Application ${applicationName} 缺少本次 release-state Git revision；拒绝沿用上一轮健康状态。"
  }
  sh """#!/bin/sh
    set -eu
    test -r '${env.RELEASE_OBSERVER_KUBECONFIG}' || {
      echo 'Jenkins release observer kubeconfig 不可读。' >&2
      exit 1
    }
    # Harbor 在集群内网；正常镜像拉取约为秒级，Pod startup 通常应在 5 分钟内完成。
    # 5 分钟是硬上限，不再用长等待掩盖 Harbor、节点网络、解压、gate 或 readiness 故障。
    # Argo 明确进入 Failed/Error/Degraded 时立即 fail-closed。
    i=0
    while [ \$i -lt 60 ]; do
      state=\$(KUBECONFIG='${env.RELEASE_OBSERVER_KUBECONFIG}' kubectl -n argocd get application '${applicationName}' -o jsonpath='{.status.sync.status}|{.status.health.status}|{.status.operationState.phase}|{.status.sync.revision}' 2>&1) || {
        echo "无法读取 Argo Application ${applicationName}：\$state" >&2
        exit 1
      }
      if [ "\$state" = 'Synced|Healthy|Succeeded|${expectedRevision}' ]; then
        exit 0
      fi
      sync_status=\$(printf '%s' "\$state" | cut -d'|' -f1)
      health_status=\$(printf '%s' "\$state" | cut -d'|' -f2)
      operation_phase=\$(printf '%s' "\$state" | cut -d'|' -f3)
      if [ "\$operation_phase" = 'Failed' ] || [ "\$operation_phase" = 'Error' ] || [ "\$health_status" = 'Degraded' ]; then
        echo "Argo Application ${applicationName} 已明确失败：sync=\$sync_status health=\$health_status operation=\$operation_phase revision=\$(printf '%s' \"\$state\" | cut -d'|' -f4)" >&2
        exit 1
      fi
      i=\$((i + 1))
      sleep 5
    done
    echo "Argo Application ${applicationName} 在 5 分钟内未达到本次 ${expectedRevision} 的 Synced|Healthy|Succeeded；停止等待并检查 Harbor、节点网络、镜像解压、gate、PVC 和 readiness。" >&2
    exit 1
  """
}

def markReleaseVerified(environmentName, sourceCommit, nodeDigest, jobsDigest, gatewayDigest, j3aManagementEnabled) {
  refreshPlatformReleaseWorkspace()
  def file = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}/release-metadata.yaml"
  if (metadataValue(environmentName, 'sourceCommit') != sourceCommit || metadataValue(environmentName, 'nodeImageDigest') != nodeDigest || metadataValue(environmentName, 'jobsImageDigest') != jobsDigest || metadataValue(environmentName, 'gatewayImageDigest') != gatewayDigest || metadataValue(environmentName, 'j3aManagementEnabled') != j3aManagementEnabled) error 'release state 在验证期间变化。'
  def releaseActor = environmentName == 'prod' ? metadataValue('prod', 'releaseActor') : ''
  if (environmentName == 'prod' && !(releaseActor in ['jenkins-prod-promotion', 'jenkins-prod-rollback'])) {
    error 'prod releaseActor 非法，拒绝记录可回滚历史。'
  }
  sh """#!/bin/sh
    set -eu
    sed -i -e 's|^  verification.status: ".*"|  verification.status: "passed"|' -e 's|^  verification.sourceCommit: ".*"|  verification.sourceCommit: "${sourceCommit}"|' '${file}'
    cd '${releaseWorkspace()}'
    if [ '${environmentName}' = 'prod' ]; then
      history='apps/juhe-ai/overlays/prod/release-history.tsv'
      if [ ! -f "\$history" ]; then
        printf '# recordedAtUtc\\tactor\\tsourceCommit\\tnodeDigest\\tjobsDigest\\tgatewayDigest\\tj3aManagementEnabled\\tjenkinsBuild\\n' > "\$history"
      fi
      j3a=\$(sed -n 's/^  j3aManagementEnabled: "\\(.*\\)"/\\1/p' '${file}' | head -n1)
      if ! awk -F '\\t' -v commit='${sourceCommit}' -v node='${nodeDigest}' -v jobs='${jobsDigest}' -v gateway='${gatewayDigest}' -v j3a="\$j3a" '\$3 == commit && \$4 == node && \$5 == jobs && \$6 == gateway && (NF == 7 ? j3a == "false" : \$7 == j3a) { found = 1 } END { exit !found }' "\$history"; then
        printf '%s\\t%s\\t%s\\t%s\\t%s\\t%s\\t%s\\t%s\\n' "\$(date -u '+%Y-%m-%dT%H:%M:%SZ')" '${releaseActor}' '${sourceCommit}' '${nodeDigest}' '${jobsDigest}' '${gatewayDigest}' "\$j3a" "\${BUILD_TAG:-unknown}" >> "\$history"
      fi
    fi
    git config user.name platform-jenkins
    git config user.email jenkins@jh.huanmin.top
    git add '${file}'
    if [ '${environmentName}' = 'prod' ]; then
      git add 'apps/juhe-ai/overlays/prod/release-history.tsv'
    fi
    if git diff --cached --quiet; then
      echo 'release verification 已标记为 passed；不重复提交。'
    else
      git commit -m '[skip ci] release(juhe-ai-${environmentName}): ${sourceCommit} verified'
      GIT_SSH_COMMAND="ssh -i '${env.GITEE_WRITE_KEY}' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/usr/share/jenkins/ref/gitee-known-hosts" git push origin HEAD:'${env.RELEASE_BRANCH}'
    fi
  """
}
