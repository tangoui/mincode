#ifndef MINCODE_BROWSER_OVERLAY_DARWIN_H
#define MINCODE_BROWSER_OVERLAY_DARWIN_H

#include <stdbool.h>

int mincode_browser_mount(void *nsWindow, const char *tabId, const char *url,
                          const char *userAgent, double x, double y,
                          double width, double height);
void mincode_browser_set_bounds(void *nsWindow, const char *tabId, double x,
                                double y, double width, double height,
                                bool visible);
void mincode_browser_hide(void *nsWindow);
void mincode_browser_close(void *nsWindow);
void mincode_browser_close_tab(void *nsWindow, const char *tabId);
void mincode_browser_activate(void *nsWindow, const char *tabId);
void mincode_browser_navigate(void *nsWindow, const char *tabId,
                              const char *url);
void mincode_browser_reload(void *nsWindow, const char *tabId);
// Goes one history entry back (delta < 0) or forward; false when there is none.
bool mincode_browser_go_history(void *nsWindow, const char *tabId, int delta);
void mincode_browser_set_zoom(void *nsWindow, const char *tabId, double zoom);
char *mincode_browser_get_url(void *nsWindow, const char *tabId);
char *mincode_browser_eval(void *nsWindow, const char *tabId,
                           const char *script, int timeoutMs);
char *mincode_browser_capture(void *nsWindow, const char *tabId,
                              int timeoutMs);
char *mincode_browser_navigation_errors(void *nsWindow, const char *tabId);
int mincode_browser_toggle_devtools(void *nsWindow, const char *tabId);
void mincode_browser_clear_data(void *nsWindow);
int mincode_browser_screen_region(void *nsWindow, double x, double y,
                                  double width, double height, int *outX,
                                  int *outY, int *outW, int *outH);
bool mincode_browser_exists(void *nsWindow);
char *mincode_browser_active_tab_id(void *nsWindow);
// Answers a page dialog; 0 on success, 1 when the dialog is unknown or already answered.
int mincode_browser_complete_dialog(void *nsWindow, const char *tabId,
                                    const char *dialogId, bool accept,
                                    const char *text);

// Test hooks: a fake dialog handler that only counts its calls.
void mincode_browser_test_register_dialog(const char *tabId, const char *dialogId);
int mincode_browser_test_dialog_calls(void);
int mincode_browser_test_popup_count(void);
// Test hook: a left click delivered to the tab's web view like a real one.
void mincode_browser_test_mouse_click(void *nsWindow, const char *tabId, double x, double y);
// Test hook: finishes the tab's open file chooser with path (NULL = cancel);
// -1 when none is open, else bit 0 = multiple, bit 1 = directories.
int mincode_browser_test_finish_file_chooser(void *nsWindow, const char *tabId, const char *path);
// Test hook: the open file panel's message for the tab, or NULL; the caller frees it.
char *mincode_browser_test_file_chooser_message(void *nsWindow, const char *tabId);
// Test hook: the tab's open save panel message (caller frees) and suggested
// name in *name (caller frees), or NULL when none is open.
char *mincode_browser_test_save_panel(void *nsWindow, const char *tabId, char **name);
// Test hook: finishes the tab's save panel with path (NULL = cancel); -1 when none is open.
int mincode_browser_test_finish_save_panel(void *nsWindow, const char *tabId, const char *path);

#endif
