export function planChatCreateDialogFromConversationPane(input: { mobile: boolean; drawerOpen: boolean }): {
  closeDrawer: boolean
  openDialogNow: boolean
} {
  const closeDrawer = input.mobile && input.drawerOpen
  return { closeDrawer, openDialogNow: !closeDrawer }
}
