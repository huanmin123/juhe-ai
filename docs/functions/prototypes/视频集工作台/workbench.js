(() => {
  'use strict'

  const STORAGE_KEY = 'juhe-video-workbench-prototype-v2'
  const SAMPLE_VIDEO = 'assets/harbor.mp4'
  const SAMPLE_IMAGE = 'assets/harbor.jpg'
  const SAMPLE_LAST_FRAME = 'assets/harbor-last.jpg'
  const SAMPLE_AUDIO = 'assets/voice.wav'

  const models = {
    seedance25: {
      label: 'Seedance 2.5',
      helper: '本原型演示文生、参考图、参考音频、声音输出和固定样例尾帧。',
      durations: [5, 8, 10, 15, 30],
      ratios: ['16:9', '9:16', '1:1'],
      resolutions: ['720p', '480p', '1080p'],
      audioReference: true,
      audioOutput: true,
      maxReferenceImages: 10,
      maxReferenceAudios: 10,
      returnsLastFrame: true
    },
    seedance20: {
      label: 'Seedance 2.0',
      helper: '本原型演示 2.0 的声音参考组合限制。添加参考音频时，还需一张外观参考图。',
      durations: [5, 10, 15],
      ratios: ['16:9', '9:16', '1:1'],
      resolutions: ['720p', '480p', '1080p'],
      audioReference: true,
      audioOutput: true,
      maxReferenceImages: 9,
      maxReferenceAudios: 3,
      returnsLastFrame: true
    },
    wan27: {
      label: 'Wan 2.7 T2V',
      helper: '本原型只演示 Wan 2.7 T2V 纯提示词输入。已有图片或音频可留在视频集，供线下剪辑使用。',
      durations: [5, 10, 15],
      ratios: ['16:9', '9:16'],
      resolutions: ['720p', '1080p'],
      audioReference: false,
      audioOutput: false,
      maxReferenceImages: 0,
      maxReferenceAudios: 0,
      returnsLastFrame: false
    }
  }

  const dom = {}
  const state = {
    sets: [],
    activeSetId: 'blank',
    selectedAssets: [],
    prompt: '',
    promptReferences: [],
    promptParts: [],
    mentionAnchor: null,
    replacingReferenceIndex: null,
    modelId: 'seedance25',
    duration: '5',
    ratio: '16:9',
    resolution: '720p',
    audioOutput: false,
    editingShotIndex: null,
    isGenerating: false,
    noteDismissed: false
  }

  let toastTimer = null
  let generationTimer = null

  document.addEventListener('DOMContentLoaded', init)

  function init() {
    cacheDom()
    restoreState()
    bindEvents()
    renderAll()
  }

  function cacheDom() {
    dom.setName = document.querySelector('#set-name')
    dom.savedState = document.querySelector('#saved-state')
    dom.prompt = document.querySelector('#prompt-input')
    dom.promptCount = document.querySelector('#prompt-count')
    dom.mentionPicker = document.querySelector('#asset-mention-picker')
    dom.mentionOptions = document.querySelector('#mention-options')
    dom.mentionEmpty = document.querySelector('#mention-empty')
    dom.promptReferenceSummary = document.querySelector('#prompt-reference-summary')
    dom.promptReferenceConflicts = document.querySelector('#prompt-reference-conflicts')
    dom.model = document.querySelector('#model-select')
    dom.duration = document.querySelector('#duration-select')
    dom.ratio = document.querySelector('#ratio-select')
    dom.resolution = document.querySelector('#resolution-select')
    dom.audioToggle = document.querySelector('#audio-toggle')
    dom.modelNotice = document.querySelector('#model-notice')
    dom.assetConflict = document.querySelector('#asset-conflict')
    dom.conflictSummary = document.querySelector('#conflict-summary')
    dom.resolveConflicts = dom.assetConflict.querySelector('[data-action="resolve-conflicts"]')
    dom.selectedAssets = document.querySelector('#selected-assets')
    dom.libraryAssets = document.querySelector('#library-assets')
    dom.assetCount = document.querySelector('#asset-count')
    dom.shotCount = document.querySelector('#shot-count')
    dom.shotsList = document.querySelector('#shots-list')
    dom.shotsEmpty = document.querySelector('#shots-empty')
    dom.shotsSubtitle = document.querySelector('#shots-subtitle')
    dom.newShot = document.querySelector('#new-shot-button')
    dom.generateButton = document.querySelector('#generate-button')
    dom.generateLabel = document.querySelector('#generate-label')
    dom.footnote = document.querySelector('#footnote-text')
    dom.fileInput = document.querySelector('#file-input')
    dom.backdrop = document.querySelector('#dialog-backdrop')
    dom.setList = document.querySelector('#set-list')
    dom.previewBackdrop = document.querySelector('#preview-backdrop')
    dom.previewVideo = document.querySelector('#preview-video')
    dom.previewTitle = document.querySelector('#preview-title')
    dom.previewSubtitle = document.querySelector('#preview-subtitle')
    dom.previewPrompt = document.querySelector('#preview-prompt')
    dom.previewDownload = document.querySelector('#preview-download')
    dom.toast = document.querySelector('#toast')
  }

  function bindEvents() {
    document.addEventListener('click', handleAction)
    dom.prompt.addEventListener('beforeinput', event => {
      state.pendingPromptInput = {
        inputType: event.inputType,
        data: event.data,
        value: dom.prompt.value,
        selectionStart: dom.prompt.selectionStart,
        selectionEnd: dom.prompt.selectionEnd
      }
    })
    dom.prompt.addEventListener('input', () => {
      const previousPrompt = state.prompt
      const nextPrompt = dom.prompt.value
      const input = state.pendingPromptInput
      state.pendingPromptInput = null
      rebasePromptReferences(previousPrompt, nextPrompt, promptInputEdit(previousPrompt, nextPrompt, input))
      state.prompt = nextPrompt
      syncPromptParts()
      updatePromptCount()
      renderPromptReferences()
      saveState()
      updateGenerateState()
      const caret = dom.prompt.selectionStart ?? nextPrompt.length
      if (input?.inputType === 'insertText' && input.data === '@' && nextPrompt[caret - 1] === '@' && state.replacingReferenceIndex === null) {
        openAssetMentionPicker(caret - 1)
      } else if (dom.mentionPicker && !dom.mentionPicker.hidden) {
        closeAssetMentionPicker()
      }
    })
    dom.prompt.addEventListener('keydown', handleMentionKeydown)
    dom.model.addEventListener('change', () => {
      state.modelId = dom.model.value
      renderSettings()
      saveState()
    })
    dom.duration.addEventListener('change', () => { state.duration = dom.duration.value; renderSettings(); saveState() })
    dom.ratio.addEventListener('change', () => { state.ratio = dom.ratio.value; renderSettings(); saveState() })
    dom.resolution.addEventListener('change', () => { state.resolution = dom.resolution.value; renderSettings(); saveState() })
    dom.audioToggle.addEventListener('change', () => { state.audioOutput = dom.audioToggle.checked; renderSettings(); saveState() })
    dom.fileInput.addEventListener('change', handleFileUpload)
    dom.backdrop.addEventListener('click', event => { if (event.target === dom.backdrop) closeDialog() })
    dom.previewBackdrop.addEventListener('click', event => { if (event.target === dom.previewBackdrop) closePreview() })
    document.addEventListener('keydown', event => {
      if (event.key === 'Escape') { closeDialog(); closePreview() }
    })
    document.addEventListener('pointerdown', event => {
      if (!dom.mentionPicker.hidden && !dom.mentionPicker.contains(event.target) && event.target !== dom.prompt && !event.target.closest('[data-action="open-asset-mentions"]')) closeAssetMentionPicker()
    })
  }

  function handleAction(event) {
    const actionTarget = event.target.closest('[data-action]')
    if (!actionTarget || actionTarget.disabled) return
    const action = actionTarget.dataset.action
    if (action === 'dismiss-note') { actionTarget.closest('.demo-note').remove(); state.noteDismissed = true; saveState(); return }
    if (action === 'open-sets' || action === 'open-library') { openSetDialog(); return }
    if (action === 'close-dialog') { closeDialog(); return }
    if (action === 'close-preview') { closePreview(); return }
    if (action === 'new-set') { createSet(); return }
    if (action === 'rename-set') { renameActiveSet(); return }
    if (action === 'add-asset') { dom.fileInput.click(); return }
    if (action === 'open-asset-mentions') { openAssetMentionPicker(); return }
    if (action === 'close-asset-mentions') { closeAssetMentionPicker(); return }
    if (action === 'insert-mentioned-asset') { insertMentionedAsset(actionTarget.dataset.assetId); return }
    if (action === 'rebind-prompt-reference') { beginReferenceRebind(Number(actionTarget.dataset.referenceIndex)); return }
    if (action === 'keep-reference-as-text') { keepReferenceAsText(Number(actionTarget.dataset.referenceIndex)); return }
    if (action === 'remove-prompt-reference') { removePromptReference(Number(actionTarget.dataset.referenceIndex)); return }
    if (action === 'pick-sample') { addSampleAssets(); return }
    if (action === 'resolve-conflicts') { focusFirstConflict(); return }
    if (action === 'apply-model-defaults') { applyModelDefaults(); return }
    if (action === 'generate') { generateShot(); return }
    if (action === 'new-shot') { resetComposerForNextShot(); return }
    if (action === 'play-sample') { openPreview({ sample: true, prompt: '清晨的海湾，镜头贴近水面缓慢向前。' }); return }
    if (action === 'use-sample-tail') { useSampleTail(); return }
    if (action === 'use-shot-tail') { useShotTail(Number(actionTarget.dataset.shotIndex), Number(actionTarget.dataset.variantIndex)); return }
    if (action === 'choose-set') { chooseSet(actionTarget.dataset.setId); return }
    if (action === 'play-shot') { playShot(Number(actionTarget.dataset.shotIndex), Number(actionTarget.dataset.variantIndex || 0)); return }
    if (action === 'redraft-shot') { redraftShot(Number(actionTarget.dataset.shotIndex)); return }
    if (action === 'cancel-shot') { cancelShot(Number(actionTarget.dataset.shotIndex)); return }
    if (action === 'add-asset-to-shot') { addLibraryAsset(actionTarget.dataset.assetId); return }
    if (action === 'remove-selected-asset') { removeSelectedAsset(actionTarget.dataset.assetId); return }
    if (action === 'use-variant') { redraftVariant(Number(actionTarget.dataset.shotIndex), Number(actionTarget.dataset.variantIndex)); return }
    if (action === 'preview-audio') { previewAudio(actionTarget.dataset.assetId); return }
  }

  function defaultSets() {
    return [
      { id: 'blank', name: '未命名视频集', brief: '从一句提示词开始', assets: [], shots: [] },
      {
        id: 'sample', name: '海湾清晨 · 示例', brief: '示例视频集，含一镜和尾帧',
        assets: [
          { id: 'harbor-image', name: '海湾参考图', type: 'image', url: SAMPLE_IMAGE, role: 'reference', source: 'sample' },
          { id: 'harbor-audio', name: '声音参考样例', type: 'audio', url: SAMPLE_AUDIO, role: 'edit', source: 'sample', duration: '3.4 秒' }
        ],
        shots: [{
          prompt: '清晨的海湾，薄雾贴着水面移动，小船从画面左侧驶向远处。镜头贴近水面，缓慢向前。',
          modelId: 'seedance25', duration: '5', ratio: '16:9', resolution: '720p', audioOutput: false,
          status: 'completed', lastFrameUrl: SAMPLE_LAST_FRAME, returnsLastFrame: true, variants: [{ url: SAMPLE_VIDEO, prompt: '清晨的海湾，薄雾贴着水面移动，小船从画面左侧驶向远处。镜头贴近水面，缓慢向前。', modelId: 'seedance25', duration: '5', ratio: '16:9', resolution: '720p', audioOutput: false, returnsLastFrame: true, lastFrameUrl: SAMPLE_LAST_FRAME, assets: [] }]
        }]
      }
    ]
  }

  function restoreState() {
    let saved = null
    try { saved = JSON.parse(localStorage.getItem(STORAGE_KEY) || 'null') } catch { saved = null }
    const base = defaultSets()
    if (saved && Array.isArray(saved.sets)) {
      state.sets = saved.sets.map(set => ({
        ...set,
        assets: withoutUploadedAssets(set.assets),
        shots: (Array.isArray(set.shots) ? set.shots : []).map(persistedShot)
      }))
      state.activeSetId = saved.activeSetId || 'blank'
      loadComposerFromSet(activeSet(), saved)
      state.noteDismissed = Boolean(saved.noteDismissed)
      return
    }
    state.sets = base
  }

  function saveState() {
    persistComposerToSet()
    const serializable = {
      sets: state.sets.map(set => ({
        ...set,
        assets: withoutUploadedAssets(set.assets),
        shots: set.shots.map(persistedShot)
      })),
      activeSetId: state.activeSetId,
      prompt: state.prompt,
      promptReferences: state.promptReferences,
      modelId: state.modelId,
      duration: state.duration,
      ratio: state.ratio,
      resolution: state.resolution,
      audioOutput: state.audioOutput,
      noteDismissed: state.noteDismissed
    }
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify(serializable)) } catch { /* storage is optional in the prototype */ }
    if (dom.savedState) dom.savedState.innerHTML = '<span></span>工作区状态已保存'
  }

  function activeSet() { return state.sets.find(set => set.id === state.activeSetId) || state.sets[0] }

  function persistComposerToSet() {
    const set = activeSet()
    if (!set) return
    set.draft = {
      editingShotIndex: Number.isInteger(state.editingShotIndex) && state.editingShotIndex >= 0 && state.editingShotIndex < set.shots.length ? state.editingShotIndex : null,
      prompt: state.prompt,
      promptReferences: state.promptReferences.map(reference => ({ ...reference })),
      modelId: state.modelId,
      duration: state.duration,
      ratio: state.ratio,
      resolution: state.resolution,
      audioOutput: state.audioOutput,
      selectedAssets: state.selectedAssets.map(asset => ({ ...asset, url: asset.source === 'upload' ? undefined : asset.url }))
    }
  }

  function loadComposerFromSet(set, fallback = {}) {
    const draft = set?.draft || (set?.id === state.activeSetId ? fallback : null) || {}
    const editingShotIndex = Number.isInteger(draft.editingShotIndex) ? draft.editingShotIndex : null
    state.editingShotIndex = editingShotIndex !== null && editingShotIndex >= 0 && editingShotIndex < (set?.shots?.length || 0) ? editingShotIndex : null
    state.prompt = draft.prompt || ''
    state.promptReferences = normalizePromptReferences(draft.promptReferences)
    state.modelId = models[draft.modelId] ? draft.modelId : 'seedance25'
    state.duration = String(draft.duration || '5')
    state.ratio = draft.ratio || '16:9'
    state.resolution = draft.resolution || '720p'
    state.audioOutput = Boolean(draft.audioOutput)
    state.selectedAssets = restoreDraftAssets(draft.selectedAssets)
    syncPromptParts()
  }

  function withoutUploadedAssets(assets) {
    return (Array.isArray(assets) ? assets : []).map(asset => asset.source === 'upload' ? { ...asset, url: undefined, missing: true } : asset)
  }

  function persistedShot(shot) {
    const persisted = {
      ...shot,
      assets: withoutUploadedAssets(shot.assets),
      variants: (Array.isArray(shot.variants) ? shot.variants : []).map(variant => ({
        ...variant,
        assets: withoutUploadedAssets(variant.assets)
      }))
    }
    delete persisted._pendingVariant
    delete persisted._pendingPreviousStatus
    if (persisted.status === 'pending') persisted.status = persisted.variants.length ? 'completed' : 'cancelled'
    return persisted
  }

  function renderAll() {
    const set = activeSet()
    if (!set) return
    dom.setName.textContent = set.name
    dom.prompt.value = state.prompt
    dom.model.value = state.modelId
    renderSettings()
    updatePromptCount()
    renderLibrary()
    renderSelectedAssets()
    renderPromptReferences()
    renderShots()
    updateGenerateState()
    if (!state.noteDismissed) { /* kept in markup */ } else { document.querySelector('.demo-note')?.remove() }
  }

  function renderSettings() {
    const model = models[state.modelId]
    fillSelect(dom.duration, model.durations.map(value => [String(value), `${value} 秒`]), state.duration, Number(state.duration), ' 秒')
    fillSelect(dom.ratio, model.ratios.map(value => [value, value]), state.ratio, state.ratio, '')
    fillSelect(dom.resolution, model.resolutions.map(value => [value, value]), state.resolution, state.resolution, '')
    dom.audioToggle.checked = state.audioOutput
    const invalidSettings = collectSettingConflicts()
    dom.modelNotice.hidden = invalidSettings.length === 0 && state.modelId === 'seedance25'
    dom.modelNotice.querySelector('span').textContent = invalidSettings.length ? `${model.helper} 当前设置需要处理：${invalidSettings.join('、')}。你可以逐项重选，或应用当前型号的推荐值。` : model.helper
    const recommendButton = dom.modelNotice.querySelector('[data-action="apply-model-defaults"]')
    recommendButton.hidden = invalidSettings.length === 0
    const conflict = [...collectAssetConflicts(), ...getPromptContentConflicts(), ...getPromptReferenceIssues().map(issue => `${issue.reference.displayToken || '素材引用'}：${issue.reason}`)]
    dom.assetConflict.hidden = conflict.length === 0
    dom.conflictSummary.textContent = conflict.length ? conflict.join('、') : ''
    if (dom.resolveConflicts) {
      dom.resolveConflicts.textContent = '定位第一个问题'
    }
    document.querySelector('#audio-setting').hidden = false
    document.querySelector('.audio-copy span').textContent = model.audioOutput ? '开启后由模型生成同步音效与人声' : '本型号在演示中不支持声音输出，可关闭后继续'
    updateGenerateState()
    updateFootnote()
    renderPromptReferences()
  }

  function fillSelect(select, options, selected, displayValue = selected, suffix = '') {
    const hasSelected = options.some(([value]) => value === selected)
    const unsupported = hasSelected ? '' : `<option value="${escapeHtml(selected)}">${escapeHtml(displayValue)}${suffix}（当前型号不支持，请重选）</option>`
    select.innerHTML = unsupported + options.map(([value, label]) => `<option value="${escapeHtml(value)}">${escapeHtml(label)}</option>`).join('')
    select.value = selected
  }

  function collectSettingConflicts() {
    const model = models[state.modelId]
    const conflicts = []
    if (!model.durations.includes(Number(state.duration))) conflicts.push(`${state.duration} 秒时长`)
    if (!model.ratios.includes(state.ratio)) conflicts.push(`${state.ratio} 画幅`)
    if (!model.resolutions.includes(state.resolution)) conflicts.push(`${state.resolution} 分辨率`)
    if (state.audioOutput && !model.audioOutput) conflicts.push('声音输出')
    return conflicts
  }

  function applyModelDefaults() {
    const model = models[state.modelId]
    state.duration = String(model.durations[0])
    state.ratio = model.ratios[0]
    state.resolution = model.resolutions[0]
    state.audioOutput = false
    renderSettings(); saveState(); showToast('已应用当前型号推荐的基础参数')
  }

  function renderLibrary() {
    const assets = activeSet().assets
    dom.assetCount.textContent = `${assets.length} 项`
    if (!assets.length) { dom.libraryAssets.innerHTML = '<div class="library-empty">当前视频集还没有素材</div>'; return }
    dom.libraryAssets.innerHTML = assets.map(asset => {
      const thumb = asset.type === 'image' && asset.url ? `<img src="${escapeAttr(asset.url)}" alt="">` : asset.type === 'image' ? '<span>图</span>' : '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 18V6l10-2v12"/><circle cx="6" cy="18" r="2.5"/><circle cx="16" cy="16" r="2.5"/></svg>'
      const info = asset.missing ? '本地文件未恢复，请重新上传' : asset.type === 'audio' ? `音频 · ${asset.duration || '可试听'}` : '图片 · 可作参考'
      const preview = asset.type === 'audio' && !asset.missing ? `<button class="library-listen" type="button" data-action="preview-audio" data-asset-id="${escapeAttr(asset.id)}" title="试听声音素材" aria-label="试听 ${escapeAttr(asset.name)}"><svg viewBox="0 0 24 24"><path d="M9 18V6l10-2v12"/><circle cx="6" cy="18" r="2.5"/><circle cx="16" cy="16" r="2.5"/></svg></button>` : ''
      return `<div class="library-item ${asset.missing ? 'is-missing' : ''}"><span class="library-thumb">${thumb}</span><span class="library-item-copy"><strong title="${escapeAttr(asset.name)}">${escapeHtml(asset.name)}</strong><span>${info}</span></span><span class="library-item-actions">${preview}<button class="library-use" type="button" data-action="add-asset-to-shot" data-asset-id="${escapeAttr(asset.id)}" ${asset.missing ? 'disabled' : ''}>${asset.missing ? '需重传' : '加入本镜'}</button></span></div>`
    }).join('')
  }

  function renderSelectedAssets() {
    dom.selectedAssets.innerHTML = state.selectedAssets.map(asset => {
      const thumb = asset.type === 'image' && asset.url ? `<img src="${escapeAttr(asset.url)}" alt="">` : asset.type === 'image' ? '<span>图</span>' : '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 18V6l10-2v12"/><circle cx="6" cy="18" r="2.5"/><circle cx="16" cy="16" r="2.5"/></svg>'
      const roleOptions = asset.type === 'audio' ? [['audio', '声音参考'], ['edit', '仅供剪辑']] : [['first', '用作首帧'], ['reference', '外观参考'], ['edit', '仅供剪辑']]
      return `<div class="selected-asset ${asset.missing ? 'is-missing' : ''}"><span class="asset-preview">${thumb}</span><span class="asset-meta"><strong title="${escapeAttr(asset.name)}">${escapeHtml(asset.name)}</strong><small>${asset.missing ? '本地文件未恢复，请重新上传' : asset.type === 'audio' ? '音频' : '图片'}</small></span><select class="asset-role" aria-label="${escapeAttr(asset.name)} 的用途" data-asset-role="${escapeAttr(asset.id)}" ${asset.missing ? 'disabled' : ''}>${roleOptions.map(([value, label]) => `<option value="${value}" ${asset.role === value ? 'selected' : ''}>${label}</option>`).join('')}</select><button class="remove-asset" type="button" aria-label="移除 ${escapeAttr(asset.name)}" data-action="remove-selected-asset" data-asset-id="${escapeAttr(asset.id)}">×</button></div>`
    }).join('')
    dom.selectedAssets.querySelectorAll('[data-asset-role]').forEach(select => select.addEventListener('change', () => {
      const asset = state.selectedAssets.find(item => item.id === select.dataset.assetRole)
      if (asset) { asset.role = select.value; renderSettings(); renderPromptReferences(); saveState() }
    }))
  }

  function renderShots() {
    const shots = activeSet().shots
    dom.shotCount.textContent = `第 ${shots.length + 1} 镜`
    dom.shotsEmpty.hidden = shots.length > 0
    dom.shotsList.innerHTML = shots.map((shot, index) => renderShot(shot, index)).join('')
    dom.newShot.disabled = shots.length === 0 || state.isGenerating
    dom.shotsSubtitle.textContent = shots.length ? `${shots.length} 个镜头 · 可继续生成下一镜或再生成一版` : '生成的镜头会按顺序排列在这里'
  }

  function renderShot(shot, index) {
    const model = models[shot.modelId] || models.seedance25
    const isPending = shot.status === 'pending'
    const currentVariantIndex = Math.max(0, (shot.variants || []).length - 1)
    const current = shot.variants?.[currentVariantIndex]
    const thumb = current?.url || SAMPLE_IMAGE
    const status = isPending ? '生成中' : shot.status === 'cancelled' ? '已取消' : '已完成'
    const variants = (shot.variants || []).length > 1 ? `<div class="variant-row"><span>${shot.variants.length} 个版本</span>${shot.variants.map((variant, variantIndex) => {
      const tailAction = variant.returnsLastFrame === true && variant.lastFrameUrl ? `<button type="button" data-action="use-shot-tail" data-shot-index="${index}" data-variant-index="${variantIndex}" aria-label="用版本 ${variantIndex + 1} 尾帧创建下一镜" title="用版本 ${variantIndex + 1} 尾帧创建下一镜"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14M13 6l6 6-6 6"/></svg></button>` : ''
      return `<span class="variant-item"><button type="button" data-action="play-shot" data-shot-index="${index}" data-variant-index="${variantIndex}" aria-label="预览版本 ${variantIndex + 1}" title="预览版本 ${variantIndex + 1}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m8 5 11 7-11 7z"/></svg><span>版本 ${variantIndex + 1}</span></button><button type="button" data-action="use-variant" data-shot-index="${index}" data-variant-index="${variantIndex}" aria-label="基于版本 ${variantIndex + 1} 重做" title="基于此版本重做"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 0 0-14.7-4L4 9M4 5v4h4"/><path d="M4 13a8 8 0 0 0 14.7 4L20 15m0 4v-4h-4"/></svg></button>${tailAction}</span>`
    }).join('')}</div>` : ''
    const currentSupportsTail = current?.returnsLastFrame
    const currentTailUrl = current?.lastFrameUrl
    const tailButton = (shot.variants || []).length === 1 && !isPending && shot.status !== 'cancelled' && currentSupportsTail === true && currentTailUrl ? `<button type="button" aria-label="用版本 ${currentVariantIndex + 1} 尾帧创建下一镜" title="用版本 ${currentVariantIndex + 1} 尾帧创建下一镜" data-action="use-shot-tail" data-shot-index="${index}" data-variant-index="${currentVariantIndex}"><svg viewBox="0 0 24 24"><path d="M5 12h14M13 6l6 6-6 6"/></svg></button>` : ''
    const playButton = !isPending && shot.status !== 'cancelled' ? `<button type="button" aria-label="播放第 ${index + 1} 镜最新版本" title="预览最新版本" data-action="play-shot" data-shot-index="${index}" data-variant-index="${currentVariantIndex}"><svg viewBox="0 0 24 24"><path d="m8 5 11 7-11 7z"/></svg></button>` : ''
    return `<article class="shot-card"><div class="shot-thumb"><img src="${escapeAttr(thumb)}" alt=""><span class="shot-thumb-status ${isPending ? 'is-pending' : ''}">${status}</span>${isPending ? '<span class="shot-spinner" aria-label="生成中"></span>' : playButton}</div><div class="shot-body"><div class="shot-body-top"><span class="shot-number">镜头 ${String(index + 1).padStart(2, '0')}</span><span class="shot-number">${escapeHtml(model.label)}</span></div><h3>${escapeHtml(shot.prompt)}</h3><div class="shot-params"><span>${escapeHtml(shot.duration)} 秒</span><span>${escapeHtml(shot.ratio)}</span><span>${escapeHtml(shot.resolution)}</span><span>${shot.audioOutput ? '有声' : '静默'}</span></div>${variants}<div class="shot-foot"><span>${isPending ? '示例任务处理中…' : shot.status === 'cancelled' ? '已取消的本地示例任务' : '样片链接可播放 · 示例素材'}</span><div class="shot-actions">${tailButton}${isPending ? `<button type="button" aria-label="取消生成" title="取消生成" data-action="cancel-shot" data-shot-index="${index}"><svg viewBox="0 0 24 24"><path d="M6 6l12 12M18 6 6 18"/></svg></button>` : shot.status !== 'cancelled' ? `<button type="button" aria-label="再生成一版" title="再生成一版" data-action="redraft-shot" data-shot-index="${index}"><svg viewBox="0 0 24 24"><path d="M20 11a8 8 0 0 0-14.7-4L4 9M4 5v4h4"/><path d="M4 13a8 8 0 0 0 14.7 4L20 15m0 4v-4h-4"/></svg></button>` : ''}</div></div></div></article>`
  }

  function updatePromptCount() { dom.promptCount.textContent = `${dom.prompt.value.length} / 4000` }

  function normalizePromptReferences(references) {
    if (!Array.isArray(references)) return []
    return references.filter(reference => reference && typeof reference.assetId === 'string').map(reference => ({
      assetId: reference.assetId,
      displayToken: typeof reference.displayToken === 'string' ? reference.displayToken : '',
      start: Number.isInteger(reference.start) ? reference.start : -1,
      end: Number.isInteger(reference.end) ? reference.end : -1
    })).sort((a, b) => a.start - b.start)
  }

  function findTextEdit(previous, next) {
    let start = 0
    while (start < previous.length && start < next.length && previous[start] === next[start]) start += 1
    let suffix = 0
    while (suffix < previous.length - start && suffix < next.length - start && previous[previous.length - 1 - suffix] === next[next.length - 1 - suffix]) suffix += 1
    return { start, oldEnd: previous.length - suffix, newEnd: next.length - suffix }
  }

  function promptInputEdit(previous, next, input) {
    if (!input || input.value !== previous || input.inputType.startsWith('history')) {
      const edit = findTextEdit(previous, next)
      return { ...edit, ambiguous: Boolean(input?.inputType.startsWith('history')) }
    }
    let start = input.selectionStart
    let oldEnd = input.selectionEnd
    const inputType = input.inputType
    const newCaret = dom.prompt.selectionStart ?? start
    if (start === oldEnd && inputType === 'deleteContentBackward') {
      start = Math.min(start, newCaret)
    } else if (start === oldEnd && inputType === 'deleteContentForward') {
      oldEnd = start + Math.max(0, previous.length - next.length)
    }
    const newEnd = next.length - (previous.length - oldEnd)
    const prefixMatches = next.slice(0, start) === previous.slice(0, start)
    const suffixMatches = next.slice(newEnd) === previous.slice(oldEnd)
    if (start >= 0 && oldEnd >= start && newEnd >= start && prefixMatches && suffixMatches) return { start, oldEnd, newEnd }
    return { ...findTextEdit(previous, next), ambiguous: true }
  }

  function rebasePromptReferences(previous, next, editHint = null) {
    const edit = editHint || findTextEdit(previous, next)
    const inserted = edit.start === edit.oldEnd
    const delta = edit.newEnd - edit.oldEnd
    state.promptReferences = state.promptReferences.filter(reference => {
      if (reference.start < 0 || reference.end < reference.start) return true
      const overlaps = inserted
        ? edit.start > reference.start && edit.start < reference.end
        : edit.start < reference.end && edit.oldEnd > reference.start
      if (overlaps) return false
      if (reference.start >= edit.oldEnd && (inserted ? reference.start >= edit.start : true)) {
        reference.start += delta
        reference.end += delta
      }
      return next.slice(reference.start, reference.end) === reference.displayToken
    }).sort((a, b) => a.start - b.start)
  }

  function syncPromptParts() {
    let cursor = 0
    const parts = []
    for (const reference of [...state.promptReferences].sort((a, b) => a.start - b.start)) {
      if (reference.start < cursor || reference.end < reference.start || state.prompt.slice(reference.start, reference.end) !== reference.displayToken) continue
      const text = state.prompt.slice(cursor, reference.start)
      if (text) parts.push({ type: 'text', text })
      parts.push({ type: 'asset', assetId: reference.assetId })
      cursor = reference.end
    }
    const remainder = state.prompt.slice(cursor)
    if (remainder) parts.push({ type: 'text', text: remainder })
    if (!parts.length && state.prompt) parts.push({ type: 'text', text: state.prompt })
    state.promptParts = parts
    return parts
  }

  function getPromptReferenceIssues() {
    const model = models[state.modelId]
    const images = state.selectedAssets.filter(asset => asset.type === 'image' && asset.role === 'reference')
    return state.promptReferences.map((reference, index) => {
      const asset = state.selectedAssets.find(item => item.id === reference.assetId)
      let reason = ''
      if (reference.start < 0 || reference.end < reference.start || state.prompt.slice(reference.start, reference.end) !== reference.displayToken) reason = '提示词中的引用位置已失效'
      else if (!asset) reason = '素材已移出本镜'
      else if (asset.missing || !asset.url) reason = '本地素材文件未恢复，请重新上传'
      else if (asset.type === 'image' && asset.role === 'first') reason = '首帧是结构输入，不能作为提示词引用'
      else if (asset.role === 'edit') reason = '素材用途已改为仅供剪辑'
      else if (asset.type === 'image' && asset.role !== 'reference') reason = '图片用途不是外观参考'
      else if (asset.type === 'audio') reason = '声音参考不支持提示词指名'
      else if (asset.type === 'image' && !model.maxReferenceImages) reason = `${model.label} 不接收参考图片`
      else if (asset.type === 'image' && images.indexOf(asset) >= model.maxReferenceImages) reason = `超过 ${model.label} 的参考图片数量上限`
      return reason ? { index, reference, reason } : null
    }).filter(Boolean)
  }

  function getPromptCandidates() {
    const model = models[state.modelId]
    const images = state.selectedAssets.filter(asset => asset.type === 'image' && asset.role === 'reference')
    const firstFrame = state.selectedAssets.some(asset => asset.type === 'image' && asset.role === 'first')
    const imageCapacityOk = images.length <= model.maxReferenceImages
    const candidates = state.selectedAssets.filter(asset => asset.type === 'image' && asset.role === 'reference' && model.maxReferenceImages > 0 && imageCapacityOk && !firstFrame)
    const counts = new Map()
    const seen = new Map()
    candidates.forEach(asset => counts.set(asset.name, (counts.get(asset.name) || 0) + 1))
    return candidates.map(asset => {
      const ordinal = (seen.get(asset.name) || 0) + 1
      seen.set(asset.name, ordinal)
      const suffix = counts.get(asset.name) > 1 ? ` #${ordinal}` : ''
      return { asset, displayToken: `@${asset.name}${suffix}` }
    })
  }

  function openAssetMentionPicker(triggerStart = null, triggerEnd = null) {
    if (state.isGenerating) { showToast('当前生成任务完成或取消后再编辑素材引用'); return }
    if (state.replacingReferenceIndex !== null && state.promptReferences[state.replacingReferenceIndex]) {
      const reference = state.promptReferences[state.replacingReferenceIndex]
      state.mentionAnchor = { start: reference.start, end: reference.end, referenceIndex: state.replacingReferenceIndex }
    } else if (Number.isInteger(triggerStart)) {
      state.mentionAnchor = { start: triggerStart, end: Number.isInteger(triggerEnd) ? triggerEnd : triggerStart + 1, referenceIndex: null }
    } else {
      state.mentionAnchor = { start: dom.prompt.selectionStart ?? state.prompt.length, end: dom.prompt.selectionEnd ?? state.prompt.length, referenceIndex: null }
    }
    renderAssetMentionPicker()
    dom.mentionPicker.hidden = false
  }

  function renderAssetMentionPicker() {
    const candidates = getPromptCandidates()
    state.mentionCandidates = candidates
    state.mentionActiveIndex = candidates.length ? Math.min(state.mentionActiveIndex || 0, candidates.length - 1) : -1
    dom.mentionOptions.innerHTML = candidates.map((candidate, index) => {
      const asset = candidate.asset
      const kind = '外观参考图'
      const active = index === state.mentionActiveIndex
      const thumbnail = asset.url ? `<img src="${escapeAttr(asset.url)}" alt="">` : '<span aria-hidden="true">图</span>'
      return `<button class="mention-option ${active ? 'is-active' : ''}" type="button" role="option" aria-selected="${active}" data-action="insert-mentioned-asset" data-asset-id="${escapeAttr(asset.id)}"><span class="mention-option-thumb">${thumbnail}</span><span class="mention-option-copy"><span class="mention-option-token">${escapeHtml(candidate.displayToken)}</span><span class="mention-option-meta">${kind} · ${escapeHtml(asset.name)}</span></span></button>`
    }).join('')
    dom.mentionEmpty.hidden = candidates.length > 0
  }

  function closeAssetMentionPicker() {
    if (dom.mentionPicker) dom.mentionPicker.hidden = true
    state.mentionAnchor = null
    state.replacingReferenceIndex = null
    state.mentionCandidates = []
  }

  function handleMentionKeydown(event) {
    if (dom.mentionPicker.hidden) return
    if (event.key === 'Escape') { event.preventDefault(); closeAssetMentionPicker(); return }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      const count = state.mentionCandidates.length
      if (!count) return
      const step = event.key === 'ArrowDown' ? 1 : -1
      state.mentionActiveIndex = (state.mentionActiveIndex + step + count) % count
      renderAssetMentionPicker()
    } else if (event.key === 'Enter' && state.mentionCandidates.length) {
      event.preventDefault()
      insertMentionedAsset(state.mentionCandidates[state.mentionActiveIndex].asset.id)
    }
  }

  function insertMentionedAsset(assetId) {
    const candidate = getPromptCandidates().find(item => item.asset.id === assetId)
    const anchor = state.mentionAnchor
    if (!candidate || !anchor) { closeAssetMentionPicker(); showToast('该素材已不符合当前型号的引用条件'); return }
    const start = Math.max(0, Math.min(anchor.start, state.prompt.length))
    const end = Math.max(start, Math.min(anchor.end, state.prompt.length))
    const nextPrompt = `${state.prompt.slice(0, start)}${candidate.displayToken}${state.prompt.slice(end)}`
    if (nextPrompt.length > 4000) { closeAssetMentionPicker(); showToast('提示词已达到 4000 字上限'); return }
    rebasePromptReferences(state.prompt, nextPrompt, { start, oldEnd: end, newEnd: start + candidate.displayToken.length })
    state.prompt = nextPrompt
    dom.prompt.value = nextPrompt
    state.promptReferences.push({ assetId, displayToken: candidate.displayToken, start, end: start + candidate.displayToken.length })
    state.promptReferences.sort((a, b) => a.start - b.start)
    state.replacingReferenceIndex = null
    state.mentionAnchor = null
    dom.mentionPicker.hidden = true
    state.mentionCandidates = []
    dom.prompt.focus()
    dom.prompt.setSelectionRange(start + candidate.displayToken.length, start + candidate.displayToken.length)
    syncPromptParts(); updatePromptCount(); renderPromptReferences(); updateGenerateState(); saveState()
  }

  function beginReferenceRebind(index) {
    if (!state.promptReferences[index]) return
    state.replacingReferenceIndex = index
    openAssetMentionPicker()
  }

  function keepReferenceAsText(index) {
    state.promptReferences.splice(index, 1)
    syncPromptParts(); renderPromptReferences(); updateGenerateState(); saveState()
  }

  function removePromptReference(index) {
    const reference = state.promptReferences[index]
    if (!reference) return
    if (state.prompt.slice(reference.start, reference.end) !== reference.displayToken) {
      state.promptReferences.splice(index, 1)
      syncPromptParts(); renderPromptReferences(); updateGenerateState(); saveState()
      showToast('已移除失效的引用绑定，提示词文字未改动')
      return
    }
    const nextPrompt = `${state.prompt.slice(0, reference.start)}${state.prompt.slice(reference.end)}`
    rebasePromptReferences(state.prompt, nextPrompt, { start: reference.start, oldEnd: reference.end, newEnd: reference.start })
    state.prompt = nextPrompt
    dom.prompt.value = nextPrompt
    syncPromptParts(); updatePromptCount(); renderPromptReferences(); updateGenerateState(); saveState()
  }

  function renderPromptReferences() {
    if (!dom.promptReferenceSummary) return
    const references = state.promptReferences.map((reference, index) => ({ ...reference, index })).sort((a, b) => a.start - b.start)
    const issues = getPromptReferenceIssues()
    const modelInputs = state.selectedAssets.filter(asset => asset.role === 'reference' || asset.role === 'audio')
    const firstFrame = state.selectedAssets.find(asset => asset.type === 'image' && asset.role === 'first')
    const offlineAudioCount = state.selectedAssets.filter(asset => asset.type === 'audio' && asset.role === 'edit').length
    const inputRows = []
    if (firstFrame) inputRows.push(`<li><span>首帧</span>${escapeHtml(firstFrame.name)} <small>结构输入</small></li>`)
    const imageInputs = modelInputs.filter(asset => asset.type === 'image')
    const audioInputs = modelInputs.filter(asset => asset.type === 'audio')
    imageInputs.forEach((asset, index) => inputRows.push(`<li><span>参考图 ${index + 1}</span>${escapeHtml(asset.name)}</li>`))
    audioInputs.forEach((asset, index) => inputRows.push(`<li><span>声音参考 ${index + 1}</span>${escapeHtml(asset.name)}</li>`))
    const mentionText = references.length ? references.map(reference => `${escapeHtml(reference.displayToken)} → ${escapeHtml(state.selectedAssets.find(asset => asset.id === reference.assetId)?.name || '素材不可用')}`).join('；') : '暂无提示词素材引用'
    dom.promptReferenceSummary.hidden = inputRows.length === 0 && references.length === 0 && offlineAudioCount === 0
    dom.promptReferenceSummary.innerHTML = `<strong>本次输入核对 · ${escapeHtml(models[state.modelId].label)}</strong><div class="reference-binding-line">提示词引用：${mentionText}</div>${inputRows.length ? `<ol>${inputRows.join('')}</ol>` : '<div class="reference-empty-line">当前没有送模素材</div>'}<small>重复点名同一素材不会重复上传；未点名但用途为参考的素材仍会发送。${offlineAudioCount ? ` 另有 ${offlineAudioCount} 段音频仅供剪辑。` : ''} 本原型不校验完整请求体大小或真实型号能力。</small>`
    dom.promptReferenceConflicts.hidden = issues.length === 0
    dom.promptReferenceConflicts.innerHTML = issues.map(issue => `<div class="reference-issue"><span><strong>${escapeHtml(issue.reference.displayToken || '未命名引用')}</strong><small>${escapeHtml(issue.reason)}</small></span><span class="reference-issue-actions"><button type="button" data-action="rebind-prompt-reference" data-reference-index="${issue.index}">重新绑定</button><button type="button" data-action="keep-reference-as-text" data-reference-index="${issue.index}">保留为文字</button><button type="button" data-action="remove-prompt-reference" data-reference-index="${issue.index}">删除引用</button></span></div>`).join('')
  }

  function updateGenerateState() {
    const canGenerate = hasNonEmptyPromptText() && !state.isGenerating && collectConflicts().length === 0
    dom.generateButton.disabled = !canGenerate
    dom.generateButton.classList.toggle('is-loading', state.isGenerating)
    dom.generateLabel.textContent = state.isGenerating ? '生成中…' : '生成分镜'
  }

  function updateFootnote() {
    const set = activeSet()
    dom.footnote.textContent = set.id === 'blank' && !set.shots.length ? '首次生成将自动建立视频集。可以在之后重命名。' : '本地演示会播放固定样片，实际生成由选中的模型和素材决定。'
  }

  function collectConflicts() { return [...collectSettingConflicts(), ...collectAssetConflicts(), ...getPromptContentConflicts(), ...getPromptReferenceIssues().map(issue => `${issue.reference.displayToken || '素材引用'}：${issue.reason}`)] }

  function getPromptContentConflicts() {
    return state.prompt.trim() && !hasNonEmptyPromptText() ? ['提示词至少需要一段非空文字，不能只包含素材引用'] : []
  }

  function hasNonEmptyPromptText() {
    if (!state.prompt.trim()) return false
    const parts = syncPromptParts()
    return parts.some(part => part.type === 'text' && part.text.trim())
  }

  function promptReferenceSignature(references) {
    return (references || []).map(reference => `${reference.assetId}:${reference.start}:${reference.end}:${reference.displayToken}`).join('|')
  }

  function snapshotModelInputs() {
    const selected = state.selectedAssets
    const referenceAssets = selected.filter(asset => asset.type === 'image' && asset.role === 'reference')
    const referenceAudios = selected.filter(asset => asset.type === 'audio' && asset.role === 'audio')
    const firstFrame = selected.find(asset => asset.type === 'image' && asset.role === 'first')
    const modelInputs = [
      ...referenceAssets.map((asset, index) => ({ id: asset.id, assetId: asset.id, name: asset.name, type: asset.type, role: 'reference', referenceIndex: index, source: asset.source, url: asset.source === 'upload' ? undefined : asset.url })),
      ...referenceAudios.map((asset, index) => ({ id: asset.id, assetId: asset.id, name: asset.name, type: asset.type, role: 'audio', referenceIndex: index, source: asset.source, duration: asset.duration, url: asset.source === 'upload' ? undefined : asset.url }))
    ]
    return {
      modelInputs,
      firstFrameSource: firstFrame ? { type: 'asset', assetId: firstFrame.id, name: firstFrame.name, source: firstFrame.source, url: firstFrame.source === 'upload' ? undefined : firstFrame.url } : null,
      offlineAudioAssetIds: selected.filter(asset => asset.type === 'audio' && asset.role === 'edit').map(asset => asset.id)
    }
  }

  function collectAssetConflicts() {
    const model = models[state.modelId]
    const conflicts = []
    const images = state.selectedAssets.filter(asset => asset.type === 'image' && asset.role !== 'edit')
    const firstFrames = images.filter(asset => asset.role === 'first')
    const references = images.filter(asset => asset.role === 'reference')
    const audios = state.selectedAssets.filter(asset => asset.type === 'audio' && asset.role === 'audio')
    const missing = state.selectedAssets.filter(asset => asset.missing && asset.role !== 'edit')
    if (missing.length) conflicts.push(`${missing.map(asset => asset.name).join('、')} 本地文件未恢复，请重新上传或改为仅供剪辑`)
    if (state.modelId === 'wan27') {
      if (images.length || audios.length) conflicts.push('Wan 2.7 T2V 当前演示只接受提示词，已选素材请改为仅供剪辑')
      return conflicts
    }
    if (images.length > model.maxReferenceImages) conflicts.push(`参考图片最多 ${model.maxReferenceImages} 张`)
    if (audios.length > model.maxReferenceAudios) conflicts.push(`参考音频最多 ${model.maxReferenceAudios} 段`)
    if (firstFrames.length > 1) conflicts.push('本次只能指定一张首帧')
    if (firstFrames.length && references.length) conflicts.push('首帧与外观参考属于不同输入方式，请只保留一种')
    if (firstFrames.length && audios.length) conflicts.push('声音参考需要使用外观参考模式，不能和首帧同时提交')
    if (!model.audioReference && audios.length) conflicts.push('当前模型不接收声音参考')
    if (state.modelId === 'seedance20' && audios.length && references.length === 0) conflicts.push('Seedance 2.0 的声音参考需要至少一张外观参考图')
    return conflicts
  }

  function addSampleAssets() {
    const set = activeSet()
    const candidates = [
      { id: 'harbor-image', name: '海湾参考图', type: 'image', url: SAMPLE_IMAGE, role: 'reference', source: 'sample' },
      { id: 'harbor-last-image', name: '海湾小船尾帧参考图', type: 'image', url: SAMPLE_LAST_FRAME, role: 'reference', source: 'sample' },
      { id: 'harbor-audio', name: '声音参考样例', type: 'audio', url: SAMPLE_AUDIO, role: 'edit', source: 'sample', duration: '3.4 秒' }
    ]
    candidates.forEach(asset => { if (!set.assets.some(item => item.id === asset.id)) set.assets.push(asset) })
    candidates.forEach(asset => {
      if (!state.selectedAssets.some(item => item.id === asset.id)) state.selectedAssets.push({ ...set.assets.find(item => item.id === asset.id) })
    })
    renderLibrary(); renderSelectedAssets(); renderSettings(); saveState()
    showToast('已加入两张示例图和一段剪辑音轨；原有选择未被替换')
  }

  function handleFileUpload(event) {
    const files = Array.from(event.target.files || [])
    if (!files.length) return
    const set = activeSet()
    const accepted = files.filter(file => {
      const mimeType = file.type.toLowerCase()
      const isAudio = mimeType.startsWith('audio/')
      const validType = isAudio ? ['audio/wav', 'audio/x-wav', 'audio/mpeg', 'audio/mp3'].includes(mimeType) : ['image/png', 'image/jpeg', 'image/webp'].includes(mimeType)
      const maxBytes = (isAudio ? 8 : 10) * 1024 * 1024
      if (!validType || file.size > maxBytes) {
        showToast(`${file.name}：${isAudio ? '仅支持 WAV/MP3 且不超过 8 MiB' : '仅支持 PNG/JPEG/WEBP 且不超过 10 MiB'}`)
        return false
      }
      return true
    })
    accepted.forEach(file => {
      const type = file.type.startsWith('audio/') ? 'audio' : 'image'
      const asset = { id: `upload-${Date.now()}-${Math.random().toString(16).slice(2)}`, name: file.name, type, url: URL.createObjectURL(file), role: type === 'audio' ? 'edit' : 'reference', source: 'upload', duration: type === 'audio' ? '本地文件' : '' }
      set.assets.push(asset); state.selectedAssets.push(asset)
    })
    event.target.value = ''
    if (!accepted.length) return
    renderLibrary(); renderSelectedAssets(); renderSettings(); saveState()
    showToast(`已添加 ${accepted.length} 项素材。本地上传只在当前标签页有效`)
  }

  function addLibraryAsset(assetId) {
    const asset = activeSet().assets.find(item => item.id === assetId)
    if (!asset) return
    if (asset.missing || !asset.url) { showToast('这项本地素材未恢复，请重新上传后再加入本镜'); return }
    if (!state.selectedAssets.some(item => item.id === assetId)) state.selectedAssets.push({ ...asset })
    renderSelectedAssets(); renderSettings(); saveState(); showToast(`已加入本镜：${asset.name}`)
  }

  function removeSelectedAsset(assetId) {
    state.selectedAssets = state.selectedAssets.filter(asset => asset.id !== assetId)
    renderSelectedAssets(); renderSettings(); saveState()
  }

  function focusFirstConflict() {
    const settings = collectSettingConflicts()
    if (settings.length) {
      if (settings.some(item => item.includes('时长'))) dom.duration.focus()
      else if (settings.some(item => item.includes('画幅'))) dom.ratio.focus()
      else if (settings.some(item => item.includes('分辨率'))) dom.resolution.focus()
      else if (settings.some(item => item.includes('声音输出'))) dom.audioToggle.focus()
      else dom.model.focus()
      showToast('已定位第一个参数问题，请逐项检查后重试')
      return
    }
    if (getPromptContentConflicts().length) {
      dom.prompt.focus()
      showToast('请补充画面描述，不能只提交素材引用')
      return
    }
    const referenceIssue = getPromptReferenceIssues()[0]
    if (referenceIssue) {
      dom.promptReferenceConflicts.querySelector(`[data-action="rebind-prompt-reference"][data-reference-index="${referenceIssue.index}"]`)?.focus()
      showToast('已定位第一个失效引用；可重新绑定、保留为文字或删除引用')
      return
    }
    const model = models[state.modelId]
    const images = state.selectedAssets.filter(asset => asset.type === 'image' && asset.role === 'reference')
    const frames = state.selectedAssets.filter(asset => asset.type === 'image' && asset.role === 'first')
    const audios = state.selectedAssets.filter(asset => asset.type === 'audio' && asset.role === 'audio')
    let targetId = ''
    if (state.modelId === 'wan27' || images.length > model.maxReferenceImages || (frames.length && (images.length || audios.length))) targetId = state.selectedAssets.find(asset => asset.role !== 'edit')?.id || ''
    else if (frames.length > 1) targetId = frames[1].id
    else if (audios.length > model.maxReferenceAudios || !model.audioReference || (frames.length && audios.length) || (state.modelId === 'seedance20' && audios.length && !images.length)) targetId = audios[0]?.id || ''
    const roleSelect = [...dom.selectedAssets.querySelectorAll('[data-asset-role]')].find(select => select.dataset.assetRole === targetId)
    if (roleSelect) {
      roleSelect.scrollIntoView({ block: 'center', behavior: 'smooth' })
      roleSelect.focus()
      showToast('已定位第一个冲突素材；请逐项调整用途或移除')
    } else {
      dom.model.focus()
      showToast('请检查当前型号的素材组合要求')
    }
  }

  function previewAudio(assetId) {
    const asset = activeSet().assets.find(item => item.id === assetId)
    if (!asset) return
    const player = document.querySelector('#audio-preview')
    player.src = asset.url
    void player.play().then(() => showToast(`正在试听：${asset.name}`)).catch(() => showToast('此浏览器无法播放该本地音频'))
  }

  function generateShot() {
    if (state.isGenerating) { showToast('当前已有镜头正在生成'); return }
    if (!state.prompt.trim()) { dom.prompt.focus(); showToast('先写一句画面描述，再开始生成'); return }
    if (!hasNonEmptyPromptText()) { dom.prompt.focus(); showToast('提示词还需要一段文字，不能只包含素材引用'); return }
    const conflicts = collectConflicts()
    if (conflicts.length) { showToast('请先处理参数或素材冲突'); return }
    const set = activeSet()
    if (set.id === 'blank' && !set.shots.length) {
      set.id = `set-${Date.now()}`
      set.name = '新建视频集'
      state.activeSetId = set.id
      dom.setName.textContent = set.name
    }
    const inputSnapshot = snapshotModelInputs()
    const shot = { prompt: state.prompt, promptParts: syncPromptParts().map(part => ({ ...part })), promptReferences: state.promptReferences.map(reference => ({ ...reference })), modelInputs: inputSnapshot.modelInputs, firstFrameSource: inputSnapshot.firstFrameSource, offlineAudioAssetIds: inputSnapshot.offlineAudioAssetIds, modelId: state.modelId, duration: state.duration, ratio: state.ratio, resolution: state.resolution, audioOutput: state.audioOutput, status: 'pending', variants: [], assets: state.selectedAssets.map(({ id, name, type, role, source, duration, url }) => ({ id, name, type, role, source, duration, url: source === 'upload' ? undefined : url })), lastFrameUrl: models[state.modelId].returnsLastFrame ? SAMPLE_LAST_FRAME : '', returnsLastFrame: models[state.modelId].returnsLastFrame }
    const editingShotIndex = state.editingShotIndex
    const redraftTarget = editingShotIndex !== null && editingShotIndex >= 0 && editingShotIndex < set.shots.length ? set.shots[editingShotIndex] : null
    if (editingShotIndex !== null && !redraftTarget) {
      state.editingShotIndex = null
      saveState()
      showToast('原分镜已不存在，已取消重做目标；请确认后重新生成')
      return
    }
    if (redraftTarget) {
      redraftTarget._pendingPreviousStatus = redraftTarget.status
      redraftTarget.status = 'pending'
      redraftTarget._pendingVariant = shot
    } else {
      set.shots.push(shot)
    }
    const targetShot = redraftTarget || shot
    state.editingShotIndex = null
    state.isGenerating = true
    renderShots(); updateGenerateState(); saveState(); showToast('已提交本地示例任务，稍后会出现样片')
    generationTimer = window.setTimeout(() => {
      if (targetShot.status !== 'pending' || (redraftTarget && !redraftTarget._pendingVariant)) return
      targetShot.status = 'completed'
      if (redraftTarget) {
        const nextVersion = redraftTarget._pendingVariant
        delete redraftTarget._pendingVariant
        delete redraftTarget._pendingPreviousStatus
        redraftTarget.modelId = nextVersion.modelId
        redraftTarget.duration = nextVersion.duration
        redraftTarget.ratio = nextVersion.ratio
        redraftTarget.resolution = nextVersion.resolution
        redraftTarget.audioOutput = nextVersion.audioOutput
        redraftTarget.assets = nextVersion.assets
        redraftTarget.promptParts = nextVersion.promptParts
        redraftTarget.promptReferences = nextVersion.promptReferences
        redraftTarget.modelInputs = nextVersion.modelInputs
        redraftTarget.firstFrameSource = nextVersion.firstFrameSource
        redraftTarget.offlineAudioAssetIds = nextVersion.offlineAudioAssetIds
        redraftTarget.returnsLastFrame = nextVersion.returnsLastFrame
        redraftTarget.lastFrameUrl = nextVersion.lastFrameUrl
        redraftTarget.prompt = shot.prompt
        redraftTarget.variants = [...(redraftTarget.variants || []), { url: SAMPLE_VIDEO, prompt: shot.prompt, promptParts: shot.promptParts, promptReferences: shot.promptReferences, modelInputs: shot.modelInputs, firstFrameSource: shot.firstFrameSource, offlineAudioAssetIds: shot.offlineAudioAssetIds, modelId: shot.modelId, duration: shot.duration, ratio: shot.ratio, resolution: shot.resolution, audioOutput: shot.audioOutput, assets: shot.assets, returnsLastFrame: shot.returnsLastFrame, lastFrameUrl: shot.lastFrameUrl }]
      } else {
        targetShot.variants = [{ url: SAMPLE_VIDEO, prompt: shot.prompt, promptParts: shot.promptParts, promptReferences: shot.promptReferences, modelInputs: shot.modelInputs, firstFrameSource: shot.firstFrameSource, offlineAudioAssetIds: shot.offlineAudioAssetIds, modelId: shot.modelId, duration: shot.duration, ratio: shot.ratio, resolution: shot.resolution, audioOutput: shot.audioOutput, assets: shot.assets, returnsLastFrame: shot.returnsLastFrame, lastFrameUrl: shot.lastFrameUrl }]
      }
      state.isGenerating = false
      generationTimer = null
      if (state.prompt === shot.prompt && promptReferenceSignature(state.promptReferences) === promptReferenceSignature(shot.promptReferences)) {
        state.prompt = ''
        state.promptReferences = []
        syncPromptParts()
      }
      const selectedIds = state.selectedAssets.map(asset => `${asset.id}:${asset.role}`).sort().join('|')
      const submittedIds = shot.assets.map(asset => `${asset.id}:${asset.role}`).sort().join('|')
      if (selectedIds === submittedIds) state.selectedAssets = []
      renderAll(); saveState(); showToast('示例分镜已完成，可以播放或再生成一版')
    }, 2100)
  }

  function resetComposerForNextShot() {
    if (state.isGenerating) { showToast('当前有镜头正在生成，完成或取消后再编辑下一镜'); return }
    state.prompt = ''
    state.promptReferences = []
    syncPromptParts()
    state.selectedAssets = []
    state.editingShotIndex = null
    state.isGenerating = false
    dom.prompt.value = ''
    renderSelectedAssets(); updatePromptCount(); renderSettings(); updateGenerateState(); saveState(); dom.prompt.focus(); showToast('已准备下一镜')
  }

  function redraftShot(index) {
    if (state.isGenerating) { showToast('当前有镜头正在生成，完成或取消后再编辑'); return }
    const shot = activeSet().shots[index]
    if (!shot) return
    state.prompt = shot.prompt
    state.promptReferences = normalizePromptReferences(shot.promptReferences, shot.prompt)
    syncPromptParts()
    state.editingShotIndex = index
    state.modelId = shot.modelId
    state.duration = shot.duration
    state.ratio = shot.ratio
    state.resolution = shot.resolution
    state.audioOutput = Boolean(shot.audioOutput)
    state.selectedAssets = restoreDraftAssets(shot.assets)
    renderAll(); dom.prompt.focus(); showToast('已把原镜头参数带回编辑器，可以生成新版本')
    saveState()
  }

  function redraftVariant(shotIndex, variantIndex) {
    if (state.isGenerating) { showToast('当前有镜头正在生成，完成或取消后再编辑'); return }
    const shot = activeSet().shots[shotIndex]
    const variant = shot?.variants?.[variantIndex]
    if (!shot || !variant) return
    if (hasMeaningfulComposerDraft() && composerSignature(currentComposerSnapshot()) !== composerSignature(variant)) {
      const confirmed = window.confirm(`当前草稿与版本 ${variantIndex + 1} 不同。继续会用该版本的提示词、素材和参数替换当前草稿，是否继续？`)
      if (!confirmed) return
    }
    state.prompt = variant.prompt || shot.prompt || ''
    state.promptReferences = normalizePromptReferences(variant.promptReferences)
    state.modelId = models[variant.modelId] ? variant.modelId : 'seedance25'
    state.duration = String(variant.duration || '5')
    state.ratio = variant.ratio || '16:9'
    state.resolution = variant.resolution || '720p'
    state.audioOutput = Boolean(variant.audioOutput)
    state.selectedAssets = restoreDraftAssets(variant.assets)
    state.editingShotIndex = shotIndex
    syncPromptParts()
    renderAll(); dom.prompt.focus(); showToast(`已回填版本 ${variantIndex + 1}，可修改后生成新版本`)
    saveState()
  }

  function hasMeaningfulComposerDraft() {
    return Boolean(
      state.prompt.trim() ||
      state.promptReferences.length ||
      state.selectedAssets.length ||
      state.modelId !== 'seedance25' ||
      state.duration !== '5' ||
      state.ratio !== '16:9' ||
      state.resolution !== '720p' ||
      state.audioOutput
    )
  }

  function currentComposerSnapshot() {
    return {
      prompt: state.prompt,
      promptReferences: state.promptReferences,
      modelId: state.modelId,
      duration: state.duration,
      ratio: state.ratio,
      resolution: state.resolution,
      audioOutput: state.audioOutput,
      assets: state.selectedAssets
    }
  }

  function composerSignature(composer) {
    const assets = Array.isArray(composer.assets) ? composer.assets : []
    return JSON.stringify({
      prompt: composer.prompt || '',
      promptReferences: normalizePromptReferences(composer.promptReferences).map(({ assetId, displayToken, start, end }) => ({ assetId, displayToken, start, end })),
      modelId: composer.modelId || 'seedance25',
      duration: String(composer.duration || '5'),
      ratio: composer.ratio || '16:9',
      resolution: composer.resolution || '720p',
      audioOutput: Boolean(composer.audioOutput),
      assets: assets.map(asset => ({ id: asset.id, role: asset.role })).sort((a, b) => `${a.id}:${a.role}`.localeCompare(`${b.id}:${b.role}`))
    })
  }

  function cancelShot(index) {
    const shot = activeSet().shots[index]
    if (!shot || shot.status !== 'pending') return
    const hadCompletedVersion = (shot.variants || []).length > 0
    shot.status = hadCompletedVersion ? (shot._pendingPreviousStatus || 'completed') : 'cancelled'
    delete shot._pendingVariant
    delete shot._pendingPreviousStatus
    if (generationTimer) window.clearTimeout(generationTimer)
    generationTimer = null
    state.isGenerating = false
    renderShots(); updateGenerateState(); saveState(); showToast('已取消本地示例任务')
  }

  function playShot(index, variantIndex = 0) {
    const shot = activeSet().shots[index]
    const variant = shot?.variants?.[variantIndex]
    if (!variant) return
    if (!variant.url) { showToast(`版本 ${variantIndex + 1} 暂无可播放样片`); return }
    openPreview({ url: variant.url, prompt: variant.prompt, title: `镜头 ${String(index + 1).padStart(2, '0')} · ${models[variant.modelId || shot.modelId]?.label || '视频样片'}`, subtitle: `版本 ${variantIndex + 1} · 本地固定样片` })
  }

  function useSampleTail() {
    if (state.isGenerating) { showToast('当前有镜头正在生成，完成或取消后再引用尾帧'); return }
    resetComposerForNextShot()
    const set = activeSet()
    let tail = set.assets.find(asset => asset.id === 'harbor-tail')
    if (!tail) {
      tail = { id: 'harbor-tail', name: '上一镜尾帧 · 清晨海湾', type: 'image', url: SAMPLE_LAST_FRAME, role: 'first', source: 'sample-tail' }
      set.assets.push(tail)
    }
    state.selectedAssets = [{ ...tail, role: 'first' }]
    renderLibrary(); renderSelectedAssets(); renderSettings()
    state.prompt = '延续上一镜的海湾与薄雾，镜头从尾帧继续向前移动。'
    state.promptReferences = []
    syncPromptParts()
    dom.prompt.value = state.prompt
    updatePromptCount(); updateGenerateState(); saveState(); showToast('已用示例尾帧创建下一镜草稿')
  }

  function useShotTail(index, variantIndex) {
    const shot = activeSet().shots[index]
    const variant = shot?.variants?.[variantIndex]
    const supportsTail = variant?.returnsLastFrame
    const tailUrl = variant?.lastFrameUrl
    if (!shot || !variant || shot.status !== 'completed' || supportsTail !== true || !tailUrl || state.isGenerating) return
    resetComposerForNextShot()
    const set = activeSet()
    const tailId = `shot-${index}-variant-${variantIndex}-tail`
    let tail = set.assets.find(asset => asset.id === tailId)
    if (!tail) {
      tail = { id: tailId, name: `镜头 ${String(index + 1).padStart(2, '0')} · 版本 ${variantIndex + 1} 尾帧`, type: 'image', url: tailUrl, role: 'first', source: 'shot-tail', shotIndex: index, variantIndex }
      set.assets.push(tail)
    } else {
      tail.url = tailUrl
    }
    state.selectedAssets = [{ ...tail, role: 'first' }]
    state.prompt = `延续镜头 ${String(index + 1).padStart(2, '0')} 版本 ${variantIndex + 1} 的尾帧，保持场景和光线一致，镜头继续向前移动。`
    state.promptReferences = []
    syncPromptParts()
    dom.prompt.value = state.prompt
    renderLibrary(); renderSelectedAssets(); renderSettings(); updatePromptCount(); updateGenerateState(); saveState(); showToast('已引用上一镜尾帧，可继续描述下一镜')
  }

  function restoreDraftAssets(assets) {
    const library = activeSet()?.assets || []
    return (Array.isArray(assets) ? assets : []).map(asset => {
      const available = library.find(item => item.id === asset.id)
      if (available?.url && available.source === 'upload') {
        return { ...asset, ...available, role: asset.role || available.role, missing: false, url: available.url }
      }
      if (available?.url && available.source !== 'upload') {
        return { ...asset, ...available, role: asset.role || available.role, missing: Boolean(available.missing), url: available.url }
      }
      return { ...asset, missing: asset.source === 'upload' ? true : Boolean(asset.missing), url: asset.source === 'upload' ? undefined : asset.url }
    })
  }

  function openPreview(info) {
    const url = info.url || SAMPLE_VIDEO
    dom.previewVideo.src = url
    dom.previewVideo.poster = SAMPLE_IMAGE
    dom.previewTitle.textContent = info.title || '清晨的海湾'
    dom.previewSubtitle.textContent = info.subtitle || '本地固定样片 · 非实时生成'
    dom.previewPrompt.textContent = info.prompt || '示例样片'
    dom.previewDownload.href = url
    if (location.protocol === 'file:') {
      dom.previewDownload.removeAttribute('download')
      dom.previewDownload.target = '_blank'
      dom.previewDownload.rel = 'noopener'
      dom.previewDownload.lastChild.textContent = '打开样片文件'
      dom.previewDownload.title = '在新标签页中使用浏览器的“另存为”保存视频'
    } else {
      dom.previewDownload.download = 'juhe-video-sample.mp4'
    }
    dom.previewBackdrop.hidden = false
  }

  function closePreview() { dom.previewVideo.pause(); dom.previewVideo.removeAttribute('src'); dom.previewVideo.load(); dom.previewBackdrop.hidden = true }

  function openSetDialog() {
    renderSetList(); dom.backdrop.hidden = false
  }

  function renderSetList() {
    dom.setList.innerHTML = state.sets.map(set => `<button class="set-list-item ${set.id === state.activeSetId ? 'is-active' : ''}" type="button" data-action="choose-set" data-set-id="${escapeAttr(set.id)}"><span class="set-list-icon"><svg viewBox="0 0 24 24"><path d="M4 5.5h6l2 2h8v11H4z"/><path d="M4 8h16"/></svg></span><span class="set-list-copy"><strong>${escapeHtml(set.name)}</strong><span>${set.shots.length ? `${set.shots.length} 个镜头 · ${set.assets.length} 项素材` : '空集 · 从提示词开始'}</span></span>${set.id === state.activeSetId ? '<span class="set-list-check">✓</span>' : ''}</button>`).join('')
  }

  function closeDialog() { dom.backdrop.hidden = true }

  function chooseSet(id) {
    if (state.isGenerating) { showToast('当前有镜头正在生成，完成或取消后再切换视频集'); return }
    if (!state.sets.some(set => set.id === id)) return
    persistComposerToSet()
    state.activeSetId = id
    loadComposerFromSet(activeSet())
    closeDialog(); renderAll(); saveState(); showToast(`已切换到「${activeSet().name}」`)
  }

  function createSet() {
    if (state.isGenerating) { showToast('当前有镜头正在生成，完成或取消后再新建视频集'); return }
    const name = window.prompt('给新视频集起一个名字', '未命名视频集')
    if (name === null) return
    const trimmed = name.trim() || '未命名视频集'
    const set = { id: `set-${Date.now()}`, name: trimmed, brief: '', assets: [], shots: [] }
    persistComposerToSet()
    state.sets.push(set); state.activeSetId = set.id; loadComposerFromSet(set); closeDialog(); renderAll(); saveState(); showToast('已新建视频集')
  }

  function renameActiveSet() {
    const set = activeSet()
    const name = window.prompt('重命名视频集', set.name)
    if (name === null) return
    set.name = name.trim() || set.name
    dom.setName.textContent = set.name; saveState(); showToast('视频集名称已更新')
  }

  function showToast(message) {
    dom.toast.textContent = message; dom.toast.classList.add('is-visible')
    window.clearTimeout(toastTimer); toastTimer = window.setTimeout(() => dom.toast.classList.remove('is-visible'), 3000)
  }

  function escapeHtml(value) { return String(value).replace(/[&<>'"]/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[char])) }
  function escapeAttr(value) { return escapeHtml(value) }
})()
