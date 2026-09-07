import { useCallback } from "react";
import { useI18n } from "./i18n";
const messages = {
  back: ["返回工作区", "返回工作區", "Back to workspace"],
  automationBack: ["返回自动化", "返回自動化", "Back to automation"],
  listBack: ["返回列表", "返回列表", "Back to list"],
  refresh: ["刷新", "重新整理", "Refresh"],
  retry: ["重试", "重試", "Retry"],
  cancel: ["取消", "取消", "Cancel"],
  confirm: ["确认删除", "確認刪除", "Confirm deletion"],
  loading: ["正在加载…", "正在載入…", "Loading…"],
  loadFailed: ["加载失败，请重试。", "載入失敗，請重試。", "Could not load. Please retry."],
  operationFailed: ["操作未完成，请重试。", "操作未完成，請重試。", "The operation did not complete. Please retry."],
  refreshedFailed: ["操作已完成，但列表刷新失败。", "操作已完成，但列表重新整理失敗。", "Operation completed, but the list could not refresh."],
  restored: ["会话已恢复", "會話已還原", "Conversation restored"],
  deleted: ["已永久删除", "已永久刪除", "Permanently deleted"],
  clearFilters: ["清除筛选", "清除篩選", "Clear filters"],
  trashDescription: ["查看和恢复已删除的会话。", "查看與還原已刪除的會話。", "Review and restore deleted conversations."],
  purgeTitle: ["永久删除会话", "永久刪除會話", "Permanently delete conversation"],
  purgeDescription: ["删除“{name}”后无法恢复。", "刪除「{name}」後無法還原。", "Deleting “{name}” cannot be undone."],
  clearTitle: ["清空回收站", "清空回收站", "Empty trash"],
  clearDescription: ["将永久删除全部 {n} 条普通会话，包括筛选隐藏的条目。系统恢复数据不在其中。此操作无法撤销。", "將永久刪除全部 {n} 條一般會話，包括篩選隱藏的項目。系統復原資料不在其中。此操作無法復原。", "Permanently delete all {n} ordinary conversations, including filtered-out items. System recovery data is excluded. This cannot be undone."],
  batchResult: ["已删除 {success} 条，{failed} 条未完成。", "已刪除 {success} 條，{failed} 條未完成。", "Deleted {success}; {failed} did not complete."],
  retryFailed: ["仅重试失败项", "僅重試失敗項目", "Retry failed items"],
  saved: ["已保存", "已儲存", "Saved"],
  saving: ["保存中…", "儲存中…", "Saving…"],
  unsaved: ["未保存", "未儲存", "Unsaved"],
  noTasks: ["还没有自动化任务，创建一个任务开始。", "還沒有自動化任務，建立一個任務開始。", "No automations yet. Create a task to get started."],
  drafts: ["草稿", "草稿", "Drafts"],
  discard: ["放弃修改", "放棄修改", "Discard changes"],
  configuration: ["任务配置", "任務設定", "Configuration"],
  savedRun: ["按已保存配置运行", "依已儲存設定執行", "Runs with saved configuration"],
  filteredDetail: ["当前任务不在筛选结果中", "目前任務不在篩選結果中", "This task is outside the current filters"],
  conflict: ["此任务已在其他地方修改，部分字段与你的编辑冲突。", "此任務已在其他地方修改，部分欄位與你的編輯衝突。", "This task changed elsewhere. Some fields conflict with your edits."],
  missingTask: ["此任务已被删除，你的草稿仍然保留。", "此任務已被刪除，你的草稿仍然保留。", "This task was deleted. Your draft is still available."],
  reloadTask: ["加载最新配置", "載入最新設定", "Load latest configuration"],
  saveAsNew: ["另存为新任务", "另存為新任務", "Save as new task"],
  deleteTask: ["删除自动化任务", "刪除自動化任務", "Delete automation"],
  deleteTaskDescription: ["删除“{name}”后将停止未来调度，已有会话会保留。", "刪除「{name}」後將停止未來排程，已有會話會保留。", "Deleting “{name}” stops future scheduling. Existing conversations are kept."],
} as const;
export type ManagementKey = keyof typeof messages;
export function useManagementT() {
  const { locale } = useI18n();
  return useCallback((key: ManagementKey, vars?: Record<string, string | number>) => {
    const text = messages[key][locale === "zh" ? 0 : locale === "zh-TW" ? 1 : 2];
    return text.replace(/\{(\w+)\}/g, (match, name: string) => vars?.[name] === undefined ? match : String(vars[name]));
  }, [locale]);
}
