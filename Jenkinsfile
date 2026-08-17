pipeline {
  agent any

  options {
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '20'))
    skipDefaultCheckout(true)
  }

  parameters {
    booleanParam(name: 'DEPLOY_PROD', defaultValue: false, description: '仅手动运行：将已验证的 test 三镜像晋级到 prod。')
    booleanParam(name: 'ROLLBACK_PROD', defaultValue: false, description: '仅手动运行：从已验证的 prod 三镜像历史中选择一个版本回滚。')
  }

  environment {
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
    INGRESS_ENDPOINT = 'http://192.168.1.76:32080'
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
      when { expression { params.DEPLOY_PROD || rollbackRequested() } }
      steps {
        script {
          if (params.DEPLOY_PROD && rollbackRequested()) {
            error 'DEPLOY_PROD 与 ROLLBACK_PROD 不能同时选择。'
          }
        }
      }
    }

    stage('构建前端与 Node 产物') {
      when { expression { !params.DEPLOY_PROD && !rollbackRequested() } }
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
      when { expression { !params.DEPLOY_PROD && !rollbackRequested() } }
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
      when { expression { !params.DEPLOY_PROD && !rollbackRequested() } }
      steps {
        script {
          writeReleaseState('test', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, 'jenkins-ci')
        }
      }
    }

    stage('验证 test') {
      when { expression { !params.DEPLOY_PROD && !rollbackRequested() } }
      steps {
        script {
          def release = [
            sourceCommit: env.SOURCE_COMMIT,
            nodeDigest: env.NODE_DIGEST,
            jobsDigest: env.JOBS_DIGEST,
            gatewayDigest: env.GATEWAY_DIGEST
          ]
          waitForIngress('test')
          markReleaseVerified('test', release.sourceCommit, release.nodeDigest, release.jobsDigest, release.gatewayDigest)
        }
      }
    }

    stage('读取已验证 test') {
      when { expression { params.DEPLOY_PROD && !rollbackRequested() } }
      steps {
        script {
          def release = readVerifiedTestRelease()
          env.SOURCE_COMMIT = release.sourceCommit
          env.NODE_DIGEST = release.nodeDigest
          env.JOBS_DIGEST = release.jobsDigest
          env.GATEWAY_DIGEST = release.gatewayDigest
        }
      }
    }

    stage('写入 prod 晋级状态') {
      when { expression { params.DEPLOY_PROD && !rollbackRequested() } }
      steps {
        script {
          def release = readVerifiedTestRelease()
          if (release.sourceCommit != env.SOURCE_COMMIT || release.nodeDigest != env.NODE_DIGEST || release.jobsDigest != env.JOBS_DIGEST || release.gatewayDigest != env.GATEWAY_DIGEST) {
            error 'test release state 在晋级期间发生变化，拒绝写入 prod。'
          }
          writeReleaseState('prod', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, 'jenkins-prod-promotion')
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
          writeReleaseState('prod', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, 'jenkins-prod-rollback')
        }
      }
    }

    stage('验证 prod') {
      when { expression { (params.DEPLOY_PROD && !rollbackRequested()) || rollbackRequested() } }
      steps {
        script {
          waitForIngress('prod')
          markReleaseVerified('prod', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST)
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
  sh "rm -rf '${releaseWorkspace()}' && GIT_SSH_COMMAND=\"ssh -i '${env.GITEE_WRITE_KEY}' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/usr/share/jenkins/ref/gitee-known-hosts\" git clone --depth 1 --branch '${env.RELEASE_BRANCH}' '${env.PLATFORM_REPOSITORY}' '${releaseWorkspace()}'"
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
    status: metadataValue('test', 'verification.status'),
    verifiedCommit: metadataValue('test', 'verification.sourceCommit')
  ]
  if (!validCommit(release.sourceCommit) || !validDigest(release.nodeDigest) || !validDigest(release.jobsDigest) || !validDigest(release.gatewayDigest)) {
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
    gatewayDigest: metadataValue('prod', 'gatewayImageDigest')
  ]
  def candidates = [:]
  readFile(historyFile).readLines().eachWithIndex { line, index ->
    if (!line || line.startsWith('#')) {
      return
    }
    def fields = line.split('\\t', -1)
    if (fields.size() != 7) {
      error "prod release-history.tsv 第 ${index + 1} 行字段数错误。"
    }
    def sourceCommit = fields[2]
    def nodeDigest = fields[3]
    def jobsDigest = fields[4]
    def gatewayDigest = fields[5]
    if (!(fields[0] ==~ /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/) ||
        !(fields[1] in ['legacy-prod-state', 'jenkins-prod-promotion', 'jenkins-prod-rollback']) ||
        !validCommit(sourceCommit) || !validDigest(nodeDigest) || !validDigest(jobsDigest) || !validDigest(gatewayDigest) || !fields[6]) {
      error "prod release-history.tsv 第 ${index + 1} 行字段非法。"
    }
    if (sourceCommit == current.sourceCommit && nodeDigest == current.nodeDigest && jobsDigest == current.jobsDigest && gatewayDigest == current.gatewayDigest) {
      return
    }
    def label = "${fields[0]} | ${fields[6]} | ${sourceCommit} | ${nodeDigest.take(19)} / ${jobsDigest.take(19)} / ${gatewayDigest.take(19)}"
    candidates[label] = [sourceCommit: sourceCommit, nodeDigest: nodeDigest, jobsDigest: jobsDigest, gatewayDigest: gatewayDigest]
  }
  return candidates
}

def replaceDigest(file, imageName, digest) {
  def expression = 's{(- name: IMAGE\\n\\s+newName: [^\\n]+\\n\\s+digest: )sha256:[a-f0-9]{64}}{$1DIGEST}'
    .replace('IMAGE', imageName)
    .replace('DIGEST', digest)
  sh "perl -0pi -e '${expression}' '${file}'"
}

def writeReleaseState(environmentName, sourceCommit, nodeDigest, jobsDigest, gatewayDigest, actor) {
  if (!validCommit(sourceCommit) || !validDigest(nodeDigest) || !validDigest(jobsDigest) || !validDigest(gatewayDigest)) error '发布状态字段不合法。'
  refreshPlatformReleaseWorkspace()
  def overlay = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}"
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai', nodeDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-jobs', jobsDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-gateway', gatewayDigest)
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
      -e 's|^  releaseActor: ".*"|  releaseActor: "${actor}"|' \\
      -e 's|^  verification.status: ".*"|  verification.status: "pending"|' \\
      -e 's|^  verification.sourceCommit: ".*"|  verification.sourceCommit: ""|' \\
      '${overlay}/release-metadata.yaml'
    git config user.name platform-jenkins
    git config user.email jenkins@jh.huanmin.top
    git add '${overlay}/kustomization.yaml' '${overlay}/release-metadata.yaml' '${overlay}/statefulset-patch.yaml'
    if git diff --cached --quiet; then
      echo 'release state 已是目标 source commit 与不可变 digest；继续执行验证，不重复提交。'
    else
      git commit -m '[skip ci] release(juhe-ai-${environmentName}): ${sourceCommit}'
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
      if env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY curl --fail --silent --show-error --max-time 10 -H 'Host: ${host}' '${env.INGRESS_ENDPOINT}/__aisys__/api/health' >/dev/null; then exit 0; fi
      i=\$((i + 1)); sleep 5
    done
    echo '${environmentName} 入口或 Node DB-ready health 未通过。' >&2
    exit 1
  """
}

def markReleaseVerified(environmentName, sourceCommit, nodeDigest, jobsDigest, gatewayDigest) {
  refreshPlatformReleaseWorkspace()
  def file = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}/release-metadata.yaml"
  if (metadataValue(environmentName, 'sourceCommit') != sourceCommit || metadataValue(environmentName, 'nodeImageDigest') != nodeDigest || metadataValue(environmentName, 'jobsImageDigest') != jobsDigest || metadataValue(environmentName, 'gatewayImageDigest') != gatewayDigest) error 'release state 在验证期间变化。'
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
        printf '# recordedAtUtc\\tactor\\tsourceCommit\\tnodeDigest\\tjobsDigest\\tgatewayDigest\\tjenkinsBuild\\n' > "\$history"
      fi
      if ! awk -F '\\t' -v commit='${sourceCommit}' -v node='${nodeDigest}' -v jobs='${jobsDigest}' -v gateway='${gatewayDigest}' '\$3 == commit && \$4 == node && \$5 == jobs && \$6 == gateway { found = 1 } END { exit !found }' "\$history"; then
        printf '%s\\t%s\\t%s\\t%s\\t%s\\t%s\\t%s\\n' "\$(date -u '+%Y-%m-%dT%H:%M:%SZ')" '${releaseActor}' '${sourceCommit}' '${nodeDigest}' '${jobsDigest}' '${gatewayDigest}' "\${BUILD_TAG:-unknown}" >> "\$history"
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
