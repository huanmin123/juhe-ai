pipeline {
  agent any

  options {
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '20'))
    skipDefaultCheckout(true)
  }

  parameters {
    booleanParam(name: 'DEPLOY_PROD', defaultValue: false, description: '通过 Jenkins API 触发：读取 test 的 sourceCommit/digest 并写入同一版本的 prod release state；运行态观察由 Jenkins 外部完成。')
    booleanParam(name: 'REVERSE_DEPLOY_PROD', defaultValue: false, description: '本次全停机单 active 发布已禁用；传 true 会被硬拒绝，不得创建反向蓝绿 release intent。')
    booleanParam(name: 'ROLLBACK_PROD', defaultValue: false, description: '通过 Jenkins API 触发：仅写回历史 prod release state，由 Argo 异步同步；不直接操作集群。')
    string(name: 'TARGET_PROD_SOURCE_COMMIT', defaultValue: '', description: '回滚目标 sourceCommit；留空时自动选择历史中最新的上一版本。')
    string(name: 'ROLLBACK_APPROVAL_TICKET', defaultValue: '', description: '回滚必须填写受控审批单号；普通发布不得使用回滚分支。')
    string(name: 'ROLLBACK_SCHEMA_COMPATIBILITY_TICKET', defaultValue: '', description: '回滚必须填写已核对数据库 schema 前向兼容性的证据单号；不回退数据库 schema。')
    string(name: 'PROD_APPROVAL_TICKET', defaultValue: '', description: '生产晋级必须填写本次用户批准的审批单号；为空时禁止写入 prod。')
    string(name: 'PROD_FINAL_APPROVAL', defaultValue: '', description: '生产晋级或回滚必须由用户明确输入 I_APPROVE_PROD_SINGLE_ACTIVE_STOP；缺少精确确认时禁止写入 prod。')
    string(name: 'RELEASE_MODE', defaultValue: 'single-active-stop', description: '本次生产发布固定为全停机单 active；其他模式必须先完成独立 GitOps 设计与审批。')
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
    PLATFORM_REPOSITORY = 'git@gitee.com:huanminabc/k8s-juhe.git'
    RELEASE_BRANCH = 'main'
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
          if (params.RELEASE_MODE?.trim() != 'single-active-stop') {
            error '当前生产发布只允许 RELEASE_MODE=single-active-stop；蓝绿/反向切换未完成独立验收。'
          }
          if (reverseDeployRequested()) {
            error 'REVERSE_DEPLOY_PROD 已被本次全停机单 active 发布策略明确禁止；不得写入反向发布 intent。'
          }
          if (rollbackRequested() && !(params.ROLLBACK_APPROVAL_TICKET?.trim())) {
            error 'ROLLBACK_PROD 必须提供受控 ROLLBACK_APPROVAL_TICKET；缺少审批单不得写 prod。'
          }
          if (rollbackRequested() && !validApprovalTicket(params.ROLLBACK_APPROVAL_TICKET)) {
            error 'ROLLBACK_APPROVAL_TICKET 格式非法，仅允许受控审批单号字符。'
          }
          if (rollbackRequested() && !(params.ROLLBACK_SCHEMA_COMPATIBILITY_TICKET?.trim())) {
            error 'ROLLBACK_PROD 必须提供 ROLLBACK_SCHEMA_COMPATIBILITY_TICKET；未证明 schema 前向兼容不得写 prod。'
          }
          if (rollbackRequested() && !validApprovalTicket(params.ROLLBACK_SCHEMA_COMPATIBILITY_TICKET)) {
            error 'ROLLBACK_SCHEMA_COMPATIBILITY_TICKET 格式非法，仅允许受控证据单号字符。'
          }
          if (params.DEPLOY_PROD && !(params.PROD_APPROVAL_TICKET?.trim())) {
            error 'DEPLOY_PROD 必须提供本次用户批准的 PROD_APPROVAL_TICKET；缺少最终批准不得写 prod。'
          }
          if (params.DEPLOY_PROD && !validApprovalTicket(params.PROD_APPROVAL_TICKET)) {
            error 'PROD_APPROVAL_TICKET 格式非法，仅允许受控审批单号字符。'
          }
          if ((params.DEPLOY_PROD || rollbackRequested()) && params.PROD_FINAL_APPROVAL?.trim() != 'I_APPROVE_PROD_SINGLE_ACTIVE_STOP') {
            error 'DEPLOY_PROD/ROLLBACK_PROD 必须填写精确的 PROD_FINAL_APPROVAL=I_APPROVE_PROD_SINGLE_ACTIVE_STOP；缺少用户最终确认不得写 prod。'
          }
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
              --build-arg VITE_JUHE_AI_J3B_ENABLED=false \
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

    stage('读取 test release state') {
      when { expression { (params.DEPLOY_PROD || reverseDeployRequested()) && !rollbackRequested() } }
      steps {
        script {
          def release = readTestRelease(params.DEPLOY_PROD)
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
          def release = readTestRelease(true)
          env.SOURCE_COMMIT = release.sourceCommit
          env.NODE_DIGEST = release.nodeDigest
          env.JOBS_DIGEST = release.jobsDigest
          env.GATEWAY_DIGEST = release.gatewayDigest
          env.J3A_MANAGEMENT_ENABLED = release.j3aManagementEnabled
          env.TEST_RELEASE_STATE_REVISION = release.platformRevision
          env.PROD_RELEASE_STATE_REVISION = writeReleaseState('prod', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, env.J3A_MANAGEMENT_ENABLED, 'jenkins-prod-promotion', env.TEST_RELEASE_STATE_REVISION)
        }
      }
    }

    stage('选择 prod 回滚版本') {
      when { expression { rollbackRequested() } }
      steps {
        script {
          def candidates = prodRollbackCandidates()
          if (candidates.isEmpty()) {
            error '没有可回滚的历史 prod release state。'
          }
          def targetCommit = params.TARGET_PROD_SOURCE_COMMIT?.trim()
          def selected = targetCommit ? candidates.values().find { it.sourceCommit == targetCommit } : candidates.values().last()
          if (selected == null) {
            error "回滚目标 ${targetCommit} 不在历史 prod release state 中。"
          }
          env.SOURCE_COMMIT = selected.sourceCommit
          env.NODE_DIGEST = selected.nodeDigest
          env.JOBS_DIGEST = selected.jobsDigest
          env.GATEWAY_DIGEST = selected.gatewayDigest
          env.J3A_MANAGEMENT_ENABLED = selected.j3aManagementEnabled
          env.PROD_RELEASE_STATE_REVISION = writeReleaseState('prod', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, env.J3A_MANAGEMENT_ENABLED, 'jenkins-prod-rollback', selected.platformRevision)
          currentBuild.description = "prod 已写入回滚 release state，source=${env.SOURCE_COMMIT}"
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
def validSha256Hex(value) { return value ==~ /^[a-f0-9]{64}$/ }
def validApprovalTicket(value) { return value != null && value.toString() ==~ /^[A-Za-z0-9][A-Za-z0-9._:\/-]{0,127}$/ }
def validEvidenceRef(value) { return value != null && value.toString() ==~ /^(?!\/)(?!.*(?:^|\/)\.\.(?:\/|$))[A-Za-z0-9._\/-]{1,256}$/ }
def rollbackRequested() { return params.ROLLBACK_PROD }
def reverseDeployRequested() { return params.REVERSE_DEPLOY_PROD }

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
  def value = metadataValueOptional(environmentName, key)
  if (!value) error "${environmentName} release state 缺少 ${key}。"
  return value
}

def metadataValueOptional(environmentName, key) {
  def file = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}/release-metadata.yaml"
  def prefix = "  ${key}: \""
  def lines = sh(script: "grep -F -- '${prefix}' '${file}' || true", returnStdout: true)
    .readLines()
    .findAll { line -> line != null && !line.isEmpty() }
  if (lines.size() > 1) {
    error "${environmentName} release state 的 ${key} 命中 ${lines.size()} 行，拒绝使用不唯一字段。"
  }
  if (lines.isEmpty()) return ''
  def line = lines[0]
  if (!line.startsWith(prefix) || !line.endsWith('"')) {
    error "${environmentName} release state 的 ${key} 格式非法。"
  }
  return line.substring(prefix.length(), line.length() - 1)
}

def readTestRelease(boolean requireVerification = false) {
  refreshPlatformReleaseWorkspace()
  def release = [
    sourceCommit: metadataValue('test', 'sourceCommit'),
    nodeDigest: metadataValue('test', 'nodeImageDigest'),
    jobsDigest: metadataValue('test', 'jobsImageDigest'),
    gatewayDigest: metadataValue('test', 'gatewayImageDigest'),
    j3aManagementEnabled: metadataValue('test', 'j3aManagementEnabled'),
    releaseMode: metadataValue('test', 'releaseMode'),
    verificationStatus: metadataValueOptional('test', 'verification.status'),
    verificationSourceCommit: metadataValueOptional('test', 'verification.sourceCommit'),
    verificationEvidenceRef: metadataValueOptional('test', 'verification.evidenceRef'),
    verificationEvidenceManifestDigest: metadataValueOptional('test', 'verification.evidenceManifestDigest'),
    verificationVerifierIdentity: metadataValueOptional('test', 'verification.verifierIdentity'),
    verificationVerifiedAt: metadataValueOptional('test', 'verification.verifiedAt')
  ]
  release.platformRevision = sh(script: "git -C '${releaseWorkspace()}' rev-parse HEAD", returnStdout: true).trim()
  if (!validCommit(release.sourceCommit) || !validDigest(release.nodeDigest) || !validDigest(release.jobsDigest) || !validDigest(release.gatewayDigest) || !(release.j3aManagementEnabled in ['true', 'false']) || release.releaseMode != 'single-active-stop') {
    error 'test release state 未通过完整性检查。'
  }
  if (requireVerification) {
    if (release.verificationStatus != 'passed') {
      error "test release state verification.status 必须为 passed，实际为 ${release.verificationStatus ?: '<missing>'}。"
    }
    if (release.verificationSourceCommit != release.sourceCommit) {
      error 'test release state verification.sourceCommit 必须与 sourceCommit 一致。'
    }
    if (!validEvidenceRef(release.verificationEvidenceRef)) {
      error 'test release state verification.evidenceRef 必须是受控相对证据引用。'
    }
    if (!validSha256Hex(release.verificationEvidenceManifestDigest)) {
      error 'test release state verification.evidenceManifestDigest 必须是 64 位十六进制摘要。'
    }
    if (!(release.verificationVerifierIdentity ==~ /^[A-Za-z0-9._:@\/-]{1,128}$/)) {
      error 'test release state verification.verifierIdentity 必须是受控 verifier 身份。'
    }
    if (!(release.verificationVerifiedAt ==~ /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/)) {
      error 'test release state verification.verifiedAt 必须是 UTC 时间。'
    }
  }
  return release
}

def prodRollbackCandidates() {
  // 回滚必须从本次构建新鲜读取平台仓库，禁止复用旧 workspace 的 history。
  refreshPlatformReleaseWorkspace()
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
  candidates.each { key, value -> value.platformRevision = sh(script: "git -C '${releaseWorkspace()}' rev-parse HEAD", returnStdout: true).trim() }
  return candidates
}

def replaceDigest(file, imageName, digest) {
  // Only the first (active) image block is eligible. Fail closed when the
  // expected block is missing or duplicated, otherwise metadata could advance
  // while kustomization still points at a different digest.
  sh """#!/bin/sh
    set -eu
    perl -0e '
      my (\$file, \$name, \$digest) = @ARGV;
      open my \$in, "<", \$file or die "无法读取 kustomization: \$!";
      local \$/; my \$text = <\$in>; close \$in;
      my \$quoted = quotemeta(\$name);
      my \$pattern = qr{(- name: \$quoted\\n\\s+newName: [^\\n]+\\n\\s+digest: )sha256:[a-f0-9]{64}};
      my \$matches = () = \$text =~ /\$pattern/g;
      die "镜像 \$name digest 替换命中数为 \$matches，期望 1\\n" unless \$matches == 1;
      \$text =~ s{\$pattern}{\$1 . \$digest}e;
      my \$expected_pattern = qr{- name: \$quoted\\n\\s+newName: [^\\n]+\\n\\s+digest: \\Q\$digest\\E};
      my \$after_matches = () = \$text =~ /\$expected_pattern/g;
      die "镜像 \$name digest 写入后回读命中数为 \$after_matches，期望 1\\n" unless \$after_matches == 1;
      open my \$out, ">", \$file or die "无法写入 kustomization: \$!";
      print \$out \$text; close \$out;
    ' '${file}' '${imageName}' '${digest}'
  """
}

def sourceUsesDirectJ3aManagement() {
  // J3a 是迁移能力开关，不能由“某个旧实现文件存在”推断为可发布。
  // 在 migration owner 提供并验收独立 contract、Secret/schema/Ingress 和回滚证据前，
  // CI 必须保持关闭，避免普通构建静默打开 test/prod 的 J3a 管理路径。
  return false
}

def configureJ3aManagementRelease(overlay, enabled) {
  if (!(enabled in ['true', 'false'])) error 'J3a 管理 release 状态必须为 true 或 false。'
  def kustomization = "${overlay}/kustomization.yaml"
  def runtimeConfig = "${overlay}/runtime-config.env"
  sh """#!/bin/sh
    set -eu
    file='${kustomization}'
    runtime_config='${runtimeConfig}'
    route='  - j3a-management-ingressroute.yaml'
    if [ '${enabled}' = 'true' ]; then
      [ -f "\$runtime_config" ] || { echo 'J3a 启用时必须提供环境专属 runtime-config.env，拒绝写 release state' >&2; exit 1; }
      if ! grep -Fqx "\$route" "\$file"; then
        sed -i '/^  - ingress.yaml\$/a\\  - j3a-management-ingressroute.yaml' "\$file"
      fi
    else
      sed -i '/^  - j3a-management-ingressroute.yaml\$/d' "\$file"
    fi
    if [ -f "\$runtime_config" ]; then
      sed -i \
        -e 's|^JUHE_AI_PROXY_LATENCY_ENABLED=.*|JUHE_AI_PROXY_LATENCY_ENABLED=${enabled}|' \
        -e 's|^JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED=.*|JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED=${enabled}|' \
        "\$runtime_config"
      enabled_count=\$(grep -Ec '^JUHE_AI_PROXY_LATENCY_ENABLED=' "\$runtime_config" || true)
      management_enabled_count=\$(grep -Ec '^JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED=' "\$runtime_config" || true)
      [ "\$enabled_count" -eq 1 ] || { echo 'J3a enabled key replacement count must be 1' >&2; exit 1; }
      [ "\$management_enabled_count" -eq 1 ] || { echo 'J3a management enabled key replacement count must be 1' >&2; exit 1; }
      grep -Fqx 'JUHE_AI_PROXY_LATENCY_ENABLED=${enabled}' "\$runtime_config"
      grep -Fqx 'JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED=${enabled}' "\$runtime_config"
    elif [ '${enabled}' = 'true' ]; then
      echo 'J3a 启用时 runtime-config.env 不存在' >&2
      exit 1
    else
      legacy_enabled_count=\$(grep -Fxc '      - JUHE_AI_PROXY_LATENCY_ENABLED=false' "\$file" || true)
      legacy_management_enabled_count=\$(grep -Fxc '      - JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED=false' "\$file" || true)
      [ "\$legacy_enabled_count" -eq 1 ] || { echo 'J3a 关闭态缺少唯一 false 开关（旧 literals 配置）' >&2; exit 1; }
      [ "\$legacy_management_enabled_count" -eq 1 ] || { echo 'J3a 管理关闭态缺少唯一 false 开关（旧 literals 配置）' >&2; exit 1; }
    fi
    route_count=\$(grep -Fxc "\$route" "\$file" || true)
    if [ '${enabled}' = 'true' ]; then
      [ "\$route_count" -eq 1 ] || { echo 'J3a IngressRoute resource must appear exactly once when enabled' >&2; exit 1; }
    else
      [ "\$route_count" -eq 0 ] || { echo 'J3a IngressRoute resource must be absent when disabled' >&2; exit 1; }
    fi
  """
}

def writeReleaseState(environmentName, sourceCommit, nodeDigest, jobsDigest, gatewayDigest, j3aManagementEnabled, actor, expectedPlatformRevision = null) {
  if (!validCommit(sourceCommit) || !validDigest(nodeDigest) || !validDigest(jobsDigest) || !validDigest(gatewayDigest) || !(j3aManagementEnabled in ['true', 'false'])) error '发布状态字段不合法。'
  refreshPlatformReleaseWorkspace()
  if (environmentName == 'prod' && expectedPlatformRevision) {
    def currentPlatformRevision = sh(script: "git -C '${releaseWorkspace()}' rev-parse HEAD", returnStdout: true).trim()
    if (currentPlatformRevision != expectedPlatformRevision) {
      error "平台 release state 在读取后发生变化：expected=${expectedPlatformRevision}, actual=${currentPlatformRevision}；拒绝写入 prod。"
    }
    if (actor == 'jenkins-prod-promotion' &&
        (metadataValue('test', 'sourceCommit') != sourceCommit ||
         metadataValue('test', 'nodeImageDigest') != nodeDigest ||
         metadataValue('test', 'jobsImageDigest') != jobsDigest ||
         metadataValue('test', 'gatewayImageDigest') != gatewayDigest ||
         metadataValue('test', 'j3aManagementEnabled') != j3aManagementEnabled ||
         metadataValue('test', 'releaseMode') != 'single-active-stop' ||
         metadataValue('test', 'verification.status') != 'passed' ||
         metadataValue('test', 'verification.sourceCommit') != sourceCommit ||
         !validEvidenceRef(metadataValue('test', 'verification.evidenceRef')) ||
         !validSha256Hex(metadataValue('test', 'verification.evidenceManifestDigest')) ||
         !(metadataValue('test', 'verification.verifierIdentity') ==~ /^[A-Za-z0-9._:@\/-]{1,128}$/) ||
         !(metadataValue('test', 'verification.verifiedAt') ==~ /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/))) {
      error 'test release state source/digest/verification 在晋级前未保持原子一致或 verifier 未通过；拒绝写入 prod。'
    }
    if (actor in ['jenkins-prod-promotion', 'jenkins-prod-rollback'] && metadataValue('prod', 'releaseMode') != 'single-active-stop') {
      error 'prod release metadata 尚未声明 releaseMode=single-active-stop；禁止将候选写入旧的双槽/standby 发布语义。'
    }
  }
  def overlay = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}"
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai', nodeDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-jobs', jobsDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-gateway', gatewayDigest)
  configureJ3aManagementRelease(overlay, j3aManagementEnabled)
  // 槽位副本数和维护入口属于独立的 GitOps 发布控制能力。
  // release state 写入只能更新镜像/功能状态，不能把候选的停机或单 active
  // 配置静默改回双槽运行；运行态观察与切换由 Jenkins 外部的 AI/Argo 流程处理。
  sh """#!/bin/sh
    set -eu
    cd '${releaseWorkspace()}'
    metadata_file='${overlay}/release-metadata.yaml'
    metadata_tmp="\${metadata_file}.tmp.\$\$"
    sed 's/[[:cntrl:]]//g' "\${metadata_file}" > "\${metadata_tmp}"
    mv "\${metadata_tmp}" "\${metadata_file}"
    sed -i \\
      -e 's|^  sourceCommit: ".*"|  sourceCommit: "${sourceCommit}"|' \\
      -e 's|^  nodeImageDigest: ".*"|  nodeImageDigest: "${nodeDigest}"|' \\
      -e 's|^  jobsImageDigest: ".*"|  jobsImageDigest: "${jobsDigest}"|' \\
      -e 's|^  gatewayImageDigest: ".*"|  gatewayImageDigest: "${gatewayDigest}"|' \\
      -e 's|^  j3aManagementEnabled: ".*"|  j3aManagementEnabled: "${j3aManagementEnabled}"|' \\
      -e 's|^  releaseActor: ".*"|  releaseActor: "${actor}"|' \\
      "\${metadata_file}"
    assert_metadata_value() {
      key="\$1"
      expected="\$2"
      key_count=\$(grep -Ec "^  \${key}: " "\${metadata_file}" || true)
      expected_line=\$(printf '  %s: "%s"' "\${key}" "\${expected}")
      value_count=\$(grep -Fxc "\${expected_line}" "\${metadata_file}" || true)
      [ "\$key_count" -eq 1 ] || { echo "release metadata key \${key} 命中数为 \${key_count}，期望 1" >&2; exit 1; }
      [ "\$value_count" -eq 1 ] || { echo "release metadata key \${key} 回读值不匹配" >&2; exit 1; }
    }
    assert_metadata_value sourceCommit '${sourceCommit}'
    assert_metadata_value nodeImageDigest '${nodeDigest}'
    assert_metadata_value jobsImageDigest '${jobsDigest}'
    assert_metadata_value gatewayImageDigest '${gatewayDigest}'
    assert_metadata_value j3aManagementEnabled '${j3aManagementEnabled}'
    assert_metadata_value releaseActor '${actor}'
    if [ '${environmentName}' = 'test' ]; then
      assert_metadata_value releaseMode 'single-active-stop'
    fi
    if [ '${environmentName}' = 'prod' ]; then
      history='apps/juhe-ai/overlays/prod/release-history.tsv'
      if [ ! -f "\$history" ]; then
        printf '# recordedAtUtc\\tactor\\tsourceCommit\\tnodeDigest\\tjobsDigest\\tgatewayDigest\\tj3aManagementEnabled\\tjenkinsBuild\\n' > "\$history"
      fi
      if ! awk -F '\\t' -v commit='${sourceCommit}' -v node='${nodeDigest}' -v jobs='${jobsDigest}' -v gateway='${gatewayDigest}' -v j3a='${j3aManagementEnabled}' '\$3 == commit && \$4 == node && \$5 == jobs && \$6 == gateway && (NF == 7 ? j3a == "false" : \$7 == j3a) { found = 1 } END { exit !found }' "\$history"; then
        printf '%s\\t%s\\t%s\\t%s\\t%s\\t%s\\t%s\\t%s\\n' "\$(date -u '+%Y-%m-%dT%H:%M:%SZ')" '${actor}' '${sourceCommit}' '${nodeDigest}' '${jobsDigest}' '${gatewayDigest}' '${j3aManagementEnabled}' "\${BUILD_TAG:-unknown}" >> "\$history"
      fi
    fi
    git config user.name platform-jenkins
    git config user.email jenkins@jh.huanmin.top
    git add '${overlay}/kustomization.yaml' '${overlay}/release-metadata.yaml' '${overlay}/statefulset-patch.yaml'
    if [ -f '${overlay}/runtime-config.env' ]; then git add '${overlay}/runtime-config.env'; fi
    if [ -f '${overlay}/j3a-management-ingressroute.yaml' ]; then git add '${overlay}/j3a-management-ingressroute.yaml'; fi
    if [ '${environmentName}' = 'prod' ] && [ -f 'apps/juhe-ai/overlays/prod/release-history.tsv' ]; then git add 'apps/juhe-ai/overlays/prod/release-history.tsv'; fi
    if git diff --cached --quiet; then
      echo 'release state 已是目标 source commit 与不可变 digest；不重复提交。'
    else
      git commit -m '[skip ci] release(juhe-ai-${environmentName}): ${sourceCommit}'
      GIT_SSH_COMMAND="ssh -i '${env.GITEE_WRITE_KEY}' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/usr/share/jenkins/ref/gitee-known-hosts" git push origin HEAD:'${env.RELEASE_BRANCH}'
    fi
  """
  return sh(script: "git -C '${releaseWorkspace()}' rev-parse HEAD", returnStdout: true).trim()
}
