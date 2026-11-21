#ifndef MINCODE_COMPUTER_PLATFORM_DARWIN_H
#define MINCODE_COMPUTER_PLATFORM_DARWIN_H

#include <stdbool.h>
#include <stdint.h>

bool mincode_computer_supported(void);
bool mincode_computer_accessibility_trusted(bool prompt);
bool mincode_computer_screen_capture_authorized(void);
bool mincode_computer_request_screen_capture(void);
int mincode_computer_main_display(uint32_t *displayId, double *originX,
                                  double *originY, double *pointWidth,
                                  double *pointHeight, int *pixelWidth,
                                  int *pixelHeight);
char *mincode_computer_display_list(void);
char *mincode_computer_capture_display(uint32_t displayId, int timeoutMs);
int mincode_computer_cursor_position(double *x, double *y);
int mincode_computer_key_event(const char *key, bool down);
int mincode_computer_mouse_move(double x, double y);
int mincode_computer_mouse_click(double x, double y, int button, int count);
int mincode_computer_mouse_down(double x, double y, int button);
int mincode_computer_mouse_drag(double x, double y, int button);
int mincode_computer_mouse_up(double x, double y, int button);
int mincode_computer_scroll(double deltaX, double deltaY);
int mincode_computer_type_text(const char *text);
int mincode_computer_press_key(const char *key, uint32_t modifiers);
int mincode_computer_open_app(const char *application);
int mincode_computer_open_url(const char *url, const char *browser);

#endif
