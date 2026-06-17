// X11 KeySym definitions for XTest keyboard input
// Based on /usr/include/X11/keysymdef.h
//
// Note: We keep lowercase names for letter constants (e.g., XK_a, XK_b)
// to follow X11 naming conventions, even though Rust prefers UPPER_CASE.
#![allow(non_upper_case_globals)]

use std::os::raw::c_ulong;

// Function keys
pub const XK_ESCAPE: c_ulong = 0xff1b;
pub const XK_F1: c_ulong = 0xffbe;
pub const XK_F2: c_ulong = 0xffbf;
pub const XK_F3: c_ulong = 0xffc0;
pub const XK_F4: c_ulong = 0xffc1;
pub const XK_F5: c_ulong = 0xffc2;
pub const XK_F6: c_ulong = 0xffc3;
pub const XK_F7: c_ulong = 0xffc4;
pub const XK_F8: c_ulong = 0xffc5;
pub const XK_F9: c_ulong = 0xffc6;
pub const XK_F10: c_ulong = 0xffc7;
pub const XK_F11: c_ulong = 0xffc8;
pub const XK_F12: c_ulong = 0xffc9;

// Modifiers
pub const XK_SHIFT_L: c_ulong = 0xffe1;
pub const XK_SHIFT_R: c_ulong = 0xffe2;
pub const XK_CONTROL_L: c_ulong = 0xffe3;
pub const XK_CONTROL_R: c_ulong = 0xffe4;
pub const XK_CAPS_LOCK: c_ulong = 0xffe5;
pub const XK_SHIFT_LOCK: c_ulong = 0xffe6;
pub const XK_META_L: c_ulong = 0xffe7;
pub const XK_META_R: c_ulong = 0xffe8;
pub const XK_ALT_L: c_ulong = 0xffe9;
pub const XK_ALT_R: c_ulong = 0xffea;
pub const XK_SUPER_L: c_ulong = 0xffeb;
pub const XK_SUPER_R: c_ulong = 0xffec;

// Misc functions
pub const XK_BACKSPACE: c_ulong = 0xff08;
pub const XK_TAB: c_ulong = 0xff09;
pub const XK_RETURN: c_ulong = 0xff0d;
pub const XK_PAUSE: c_ulong = 0xff13;
pub const XK_SCROLL_LOCK: c_ulong = 0xff14;
pub const XK_PRINT: c_ulong = 0xff61;
pub const XK_INSERT: c_ulong = 0xff63;
pub const XK_DELETE: c_ulong = 0xffff;

// Cursor control
pub const XK_HOME: c_ulong = 0xff50;
pub const XK_LEFT: c_ulong = 0xff51;
pub const XK_UP: c_ulong = 0xff52;
pub const XK_RIGHT: c_ulong = 0xff53;
pub const XK_DOWN: c_ulong = 0xff54;
pub const XK_PAGE_UP: c_ulong = 0xff55;
pub const XK_PAGE_DOWN: c_ulong = 0xff56;
pub const XK_END: c_ulong = 0xff57;

// Keypad
pub const XK_NUM_LOCK: c_ulong = 0xff7f;
pub const XK_KP_SPACE: c_ulong = 0xff80;
pub const XK_KP_TAB: c_ulong = 0xff89;
pub const XK_KP_ENTER: c_ulong = 0xff8d;
pub const XK_KP_HOME: c_ulong = 0xff95;
pub const XK_KP_LEFT: c_ulong = 0xff96;
pub const XK_KP_UP: c_ulong = 0xff97;
pub const XK_KP_RIGHT: c_ulong = 0xff98;
pub const XK_KP_DOWN: c_ulong = 0xff99;
pub const XK_KP_PAGE_UP: c_ulong = 0xff9a;
pub const XK_KP_PAGE_DOWN: c_ulong = 0xff9b;
pub const XK_KP_END: c_ulong = 0xff9c;
pub const XK_KP_INSERT: c_ulong = 0xff9e;
pub const XK_KP_DELETE: c_ulong = 0xff9f;
pub const XK_KP_EQUAL: c_ulong = 0xffbd;
pub const XK_KP_MULTIPLY: c_ulong = 0xffaa;
pub const XK_KP_ADD: c_ulong = 0xffab;
pub const XK_KP_SEPARATOR: c_ulong = 0xffac;
pub const XK_KP_SUBTRACT: c_ulong = 0xffad;
pub const XK_KP_DECIMAL: c_ulong = 0xffae;
pub const XK_KP_DIVIDE: c_ulong = 0xffaf;
pub const XK_KP_0: c_ulong = 0xffb0;
pub const XK_KP_1: c_ulong = 0xffb1;
pub const XK_KP_2: c_ulong = 0xffb2;
pub const XK_KP_3: c_ulong = 0xffb3;
pub const XK_KP_4: c_ulong = 0xffb4;
pub const XK_KP_5: c_ulong = 0xffb5;
pub const XK_KP_6: c_ulong = 0xffb6;
pub const XK_KP_7: c_ulong = 0xffb7;
pub const XK_KP_8: c_ulong = 0xffb8;
pub const XK_KP_9: c_ulong = 0xffb9;

// Latin 1 - ASCII subset
pub const XK_SPACE: c_ulong = 0x0020;
pub const XK_EXCLAM: c_ulong = 0x0021;
pub const XK_QUOTEDBL: c_ulong = 0x0022;
pub const XK_NUMBERSIGN: c_ulong = 0x0023;
pub const XK_DOLLAR: c_ulong = 0x0024;
pub const XK_PERCENT: c_ulong = 0x0025;
pub const XK_AMPERSAND: c_ulong = 0x0026;
pub const XK_APOSTROPHE: c_ulong = 0x0027;
pub const XK_PARENLEFT: c_ulong = 0x0028;
pub const XK_PARENRIGHT: c_ulong = 0x0029;
pub const XK_ASTERISK: c_ulong = 0x002a;
pub const XK_PLUS: c_ulong = 0x002b;
pub const XK_COMMA: c_ulong = 0x002c;
pub const XK_MINUS: c_ulong = 0x002d;
pub const XK_PERIOD: c_ulong = 0x002e;
pub const XK_SLASH: c_ulong = 0x002f;

// Numbers
pub const XK_0: c_ulong = 0x0030;
pub const XK_1: c_ulong = 0x0031;
pub const XK_2: c_ulong = 0x0032;
pub const XK_3: c_ulong = 0x0033;
pub const XK_4: c_ulong = 0x0034;
pub const XK_5: c_ulong = 0x0035;
pub const XK_6: c_ulong = 0x0036;
pub const XK_7: c_ulong = 0x0037;
pub const XK_8: c_ulong = 0x0038;
pub const XK_9: c_ulong = 0x0039;

pub const XK_COLON: c_ulong = 0x003a;
pub const XK_SEMICOLON: c_ulong = 0x003b;
pub const XK_LESS: c_ulong = 0x003c;
pub const XK_EQUAL: c_ulong = 0x003d;
pub const XK_GREATER: c_ulong = 0x003e;
pub const XK_QUESTION: c_ulong = 0x003f;
pub const XK_AT: c_ulong = 0x0040;

// Uppercase letters
pub const XK_A: c_ulong = 0x0041;
pub const XK_B: c_ulong = 0x0042;
pub const XK_C: c_ulong = 0x0043;
pub const XK_D: c_ulong = 0x0044;
pub const XK_E: c_ulong = 0x0045;
pub const XK_F: c_ulong = 0x0046;
pub const XK_G: c_ulong = 0x0047;
pub const XK_H: c_ulong = 0x0048;
pub const XK_I: c_ulong = 0x0049;
pub const XK_J: c_ulong = 0x004a;
pub const XK_K: c_ulong = 0x004b;
pub const XK_L: c_ulong = 0x004c;
pub const XK_M: c_ulong = 0x004d;
pub const XK_N: c_ulong = 0x004e;
pub const XK_O: c_ulong = 0x004f;
pub const XK_P: c_ulong = 0x0050;
pub const XK_Q: c_ulong = 0x0051;
pub const XK_R: c_ulong = 0x0052;
pub const XK_S: c_ulong = 0x0053;
pub const XK_T: c_ulong = 0x0054;
pub const XK_U: c_ulong = 0x0055;
pub const XK_V: c_ulong = 0x0056;
pub const XK_W: c_ulong = 0x0057;
pub const XK_X: c_ulong = 0x0058;
pub const XK_Y: c_ulong = 0x0059;
pub const XK_Z: c_ulong = 0x005a;

pub const XK_BRACKETLEFT: c_ulong = 0x005b;
pub const XK_BACKSLASH: c_ulong = 0x005c;
pub const XK_BRACKETRIGHT: c_ulong = 0x005d;
pub const XK_ASCIICIRCUM: c_ulong = 0x005e;
pub const XK_UNDERSCORE: c_ulong = 0x005f;
pub const XK_GRAVE: c_ulong = 0x0060;

// Lowercase letters
pub const XK_a: c_ulong = 0x0061;
pub const XK_b: c_ulong = 0x0062;
pub const XK_c: c_ulong = 0x0063;
pub const XK_d: c_ulong = 0x0064;
pub const XK_e: c_ulong = 0x0065;
pub const XK_f: c_ulong = 0x0066;
pub const XK_g: c_ulong = 0x0067;
pub const XK_h: c_ulong = 0x0068;
pub const XK_i: c_ulong = 0x0069;
pub const XK_j: c_ulong = 0x006a;
pub const XK_k: c_ulong = 0x006b;
pub const XK_l: c_ulong = 0x006c;
pub const XK_m: c_ulong = 0x006d;
pub const XK_n: c_ulong = 0x006e;
pub const XK_o: c_ulong = 0x006f;
pub const XK_p: c_ulong = 0x0070;
pub const XK_q: c_ulong = 0x0071;
pub const XK_r: c_ulong = 0x0072;
pub const XK_s: c_ulong = 0x0073;
pub const XK_t: c_ulong = 0x0074;
pub const XK_u: c_ulong = 0x0075;
pub const XK_v: c_ulong = 0x0076;
pub const XK_w: c_ulong = 0x0077;
pub const XK_x: c_ulong = 0x0078;
pub const XK_y: c_ulong = 0x0079;
pub const XK_z: c_ulong = 0x007a;

pub const XK_BRACELEFT: c_ulong = 0x007b;
pub const XK_BAR: c_ulong = 0x007c;
pub const XK_BRACERIGHT: c_ulong = 0x007d;
pub const XK_ASCIITILDE: c_ulong = 0x007e;
