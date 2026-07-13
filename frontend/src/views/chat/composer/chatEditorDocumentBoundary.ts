import type { JSONContent } from '@tiptap/core'
import type { Schema } from '@tiptap/pm/model'
import { EditorState } from '@tiptap/pm/state'

export interface EditorDocumentBoundary {
  schema: Schema
  state: EditorState
  view: { updateState: (state: EditorState) => void }
}

export function replaceEditorDocumentWithoutHistory(editor: EditorDocumentBoundary, document: JSONContent): void {
  const doc = editor.schema.nodeFromJSON(document)
  editor.view.updateState(EditorState.create({
    schema: editor.schema,
    doc,
    plugins: editor.state.plugins
  }))
}
