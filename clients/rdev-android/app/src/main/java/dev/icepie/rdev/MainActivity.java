package dev.icepie.rdev;

import android.Manifest;
import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.content.res.Configuration;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.media.projection.MediaProjectionManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Environment;
import android.os.PowerManager;
import android.provider.Settings;
import android.text.InputType;
import android.view.Gravity;
import android.view.View;
import android.view.inputmethod.InputMethodManager;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

public class MainActivity extends Activity {
    private static final int REQUEST_MEDIA_PROJECTION = 1001;
    private EditText serverField;
    private EditText idField;
    private EditText passwordField;
    private TextView statusView;
    private SharedPreferences prefs;
    private String statusMessage = "本机可作为 RDev 被控端，支持终端、文件管理和远程桌面。";

    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        prefs = getSharedPreferences("rdev", MODE_PRIVATE);
        setContentView(createContentView());
        requestNotificationPermissionIfNeeded();
    }

    private View createContentView() {
        Colors colors = colors();
        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        scroll.setBackgroundColor(colors.background);

        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(dp(18), dp(20), dp(18), dp(24));
        scroll.addView(root, matchWrap());

        TextView title = text("RDev Android", 26, colors.text, Typeface.BOLD);
        title.setGravity(Gravity.CENTER_HORIZONTAL);
        root.addView(title, matchWrap());

        TextView subtitle = text("被控端 · 终端 / 文件 / 桌面控制", 14, colors.muted, Typeface.NORMAL);
        subtitle.setGravity(Gravity.CENTER_HORIZONTAL);
        subtitle.setPadding(0, dp(4), 0, dp(14));
        root.addView(subtitle, matchWrap());

        LinearLayout config = card("连接配置", "保存后启动在线服务，空密码表示开放模式。", colors);
        serverField = addInput(config, "服务器", "wss://rdev.singzer.cn", prefs.getString("server", "wss://rdev.singzer.cn"), false, colors);
        idField = addInput(config, "设备 ID", "例如 PDA3109", prefs.getString("id", defaultDeviceId()), false, colors);
        passwordField = addInput(config, "访问密码", "留空为无密码", prefs.getString("password", ""), true, colors);
        root.addView(config, sectionParams());

        LinearLayout quick = card("快速操作", "先启动在线服务；需要远程桌面时再授权录屏。", colors);
        LinearLayout firstRow = row();
        Button start = button("启动在线", true, colors);
        start.setOnClickListener(v -> startOnlineService());
        Button capture = button("授权桌面", true, colors);
        capture.setOnClickListener(v -> requestProjection());
        firstRow.addView(start, weighted());
        firstRow.addView(capture, weightedWithStartMargin());
        quick.addView(firstRow, matchWrap());

        LinearLayout secondRow = row();
        Button save = button("保存配置", false, colors);
        save.setOnClickListener(v -> saveConfig());
        Button stop = button("停止服务", false, colors);
        stop.setOnClickListener(v -> {
            stopService(new Intent(this, RDevAgentService.class));
            setStatus("在线服务已停止");
        });
        secondRow.addView(save, weighted());
        secondRow.addView(stop, weightedWithStartMargin());
        quick.addView(secondRow, topMarginParams(dp(10)));
        root.addView(quick, sectionParams());

        LinearLayout permissions = card("权限与输入", "远程触控、键盘和公开存储访问需要系统授权。", colors);
        Button accessibility = button("无障碍触控权限", false, colors);
        accessibility.setOnClickListener(v -> startActivity(new Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS)));
        permissions.addView(accessibility, matchWrap());

        LinearLayout keyboardRow = row();
        Button inputMethod = button("键盘设置", false, colors);
        inputMethod.setOnClickListener(v -> startActivity(new Intent(Settings.ACTION_INPUT_METHOD_SETTINGS)));
        Button chooseInput = button("切换键盘", false, colors);
        chooseInput.setOnClickListener(v -> {
            InputMethodManager imm = (InputMethodManager) getSystemService(Context.INPUT_METHOD_SERVICE);
            if (imm != null) imm.showInputMethodPicker();
        });
        keyboardRow.addView(inputMethod, weighted());
        keyboardRow.addView(chooseInput, weightedWithStartMargin());
        permissions.addView(keyboardRow, topMarginParams(dp(10)));

        LinearLayout systemRow = row();
        Button filesAccess = button("文件权限", false, colors);
        filesAccess.setOnClickListener(v -> requestFileAccess());
        Button battery = button("后台保活", false, colors);
        battery.setOnClickListener(v -> requestIgnoreBatteryOptimizations());
        systemRow.addView(filesAccess, weighted());
        systemRow.addView(battery, weightedWithStartMargin());
        permissions.addView(systemRow, topMarginParams(dp(10)));

        Button appSettings = button("打开系统应用设置", false, colors);
        appSettings.setOnClickListener(v -> startActivity(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:" + getPackageName()))));
        permissions.addView(appSettings, topMarginParams(dp(10)));
        root.addView(permissions, sectionParams());

        LinearLayout status = card("当前状态", "刷新页面或返回应用时自动更新权限状态。", colors);
        statusView = text("", 14, colors.text, Typeface.NORMAL);
        statusView.setLineSpacing(dp(2), 1.0f);
        status.addView(statusView, matchWrap());
        Button refresh = button("刷新状态", false, colors);
        refresh.setOnClickListener(v -> updateStatus());
        status.addView(refresh, topMarginParams(dp(12)));
        root.addView(status, sectionParams());
        updateStatus();
        return scroll;
    }

    private LinearLayout card(String title, String hint, Colors colors) {
        LinearLayout card = new LinearLayout(this);
        card.setOrientation(LinearLayout.VERTICAL);
        card.setPadding(dp(16), dp(14), dp(16), dp(16));
        card.setBackground(rounded(colors.surface, dp(18), colors.stroke));

        TextView titleView = text(title, 17, colors.text, Typeface.BOLD);
        card.addView(titleView, matchWrap());
        TextView hintView = text(hint, 13, colors.muted, Typeface.NORMAL);
        hintView.setPadding(0, dp(4), 0, dp(12));
        card.addView(hintView, matchWrap());
        return card;
    }

    private EditText addInput(LinearLayout parent, String label, String hint, String value, boolean password, Colors colors) {
        TextView labelView = text(label, 13, colors.muted, Typeface.BOLD);
        parent.addView(labelView, topMarginParams(parent.getChildCount() > 2 ? dp(10) : 0));

        EditText field = new EditText(this);
        field.setHint(hint);
        field.setSingleLine(true);
        field.setText(value);
        field.setTextColor(colors.text);
        field.setHintTextColor(colors.placeholder);
        field.setTextSize(15);
        field.setPadding(dp(12), 0, dp(12), 0);
        field.setMinHeight(dp(46));
        field.setBackground(rounded(colors.input, dp(12), colors.stroke));
        field.setInputType(password ? InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD : InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        parent.addView(field, topMarginParams(dp(6)));
        return field;
    }

    private Button button(String text, boolean primary, Colors colors) {
        Button button = new Button(this);
        button.setText(text);
        button.setAllCaps(false);
        button.setTextSize(15);
        button.setMinHeight(dp(48));
        button.setTextColor(primary ? Color.WHITE : colors.text);
        button.setBackground(rounded(primary ? colors.accent : colors.button, dp(14), primary ? colors.accent : colors.stroke));
        return button;
    }

    private TextView text(String value, int sp, int color, int style) {
        TextView view = new TextView(this);
        view.setText(value);
        view.setTextSize(sp);
        view.setTextColor(color);
        view.setTypeface(Typeface.DEFAULT, style);
        return view;
    }

    private LinearLayout row() {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        return row;
    }

    private LinearLayout.LayoutParams matchWrap() {
        return new LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
    }

    private LinearLayout.LayoutParams sectionParams() {
        LinearLayout.LayoutParams params = matchWrap();
        params.topMargin = dp(14);
        return params;
    }

    private LinearLayout.LayoutParams topMarginParams(int margin) {
        LinearLayout.LayoutParams params = matchWrap();
        params.topMargin = margin;
        return params;
    }

    private LinearLayout.LayoutParams weighted() {
        return new LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f);
    }

    private LinearLayout.LayoutParams weightedWithStartMargin() {
        LinearLayout.LayoutParams params = weighted();
        params.leftMargin = dp(10);
        return params;
    }

    private GradientDrawable rounded(int fill, int radius, int stroke) {
        GradientDrawable drawable = new GradientDrawable();
        drawable.setColor(fill);
        drawable.setCornerRadius(radius);
        drawable.setStroke(dp(1), stroke);
        return drawable;
    }

    private int dp(int value) {
        return (int) (value * getResources().getDisplayMetrics().density + 0.5f);
    }

    private void saveConfig() {
        prefs.edit()
            .putString("server", serverField.getText().toString().trim())
            .putString("id", idField.getText().toString().trim())
            .putString("password", passwordField.getText().toString())
            .apply();
        setStatus("配置已保存");
    }

    private void startOnlineService() {
        saveConfig();
        requestStoragePermissionIfNeeded();
        Intent intent = new Intent(this, RDevAgentService.class);
        if (Build.VERSION.SDK_INT >= 26) {
            startForegroundService(intent);
        } else {
            startService(intent);
        }
        setStatus("在线服务已启动：终端和文件可用；桌面控制需要录屏授权。");
    }

    private void requestProjection() {
        saveConfig();
        requestStoragePermissionIfNeeded();
        MediaProjectionManager manager = (MediaProjectionManager) getSystemService(Context.MEDIA_PROJECTION_SERVICE);
        startActivityForResult(manager.createScreenCaptureIntent(), REQUEST_MEDIA_PROJECTION);
    }

    @Override protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode != REQUEST_MEDIA_PROJECTION) return;
        if (resultCode != RESULT_OK || data == null) {
            setStatus("录屏授权已取消");
            return;
        }
        Intent intent = new Intent(this, RDevAgentService.class);
        intent.setAction(RDevAgentService.ACTION_START_CAPTURE);
        intent.putExtra(RDevAgentService.EXTRA_RESULT_CODE, resultCode);
        intent.putExtra(RDevAgentService.EXTRA_RESULT_DATA, data);
        if (Build.VERSION.SDK_INT >= 26) {
            startForegroundService(intent);
        } else {
            startService(intent);
        }
        setStatus("录屏授权已完成：有人观看桌面时会自动启动编码。当前可在 Web 端打开桌面控制。");
    }

    @Override protected void onResume() {
        super.onResume();
        updateStatus();
    }

    private void setStatus(String message) {
        statusMessage = message;
        updateStatus();
    }

    private void updateStatus() {
        if (statusView == null) return;
        statusView.setText(statusText(statusMessage));
    }

    private String statusText(String message) {
        return message
            + "\n\n触控控制：" + (RDevAccessibilityService.isActive() ? "已开启" : "未开启，需启用 RDev Remote Input")
            + "\n键盘输入：" + (RDevInputMethodService.isActive() ? "RDev HID Keyboard 已连接" : "可选，启用后文本和特殊按键更稳定")
            + "\n文件访问：" + fileAccessStatus()
            + "\n后台保活：" + batteryStatus();
    }

    private String fileAccessStatus() {
        if (Build.VERSION.SDK_INT >= 30) return Environment.isExternalStorageManager() ? "已允许全部文件" : "建议允许全部文件访问";
        if (Build.VERSION.SDK_INT >= 23) return checkSelfPermission(Manifest.permission.READ_EXTERNAL_STORAGE) == PackageManager.PERMISSION_GRANTED ? "已允许公开存储" : "需要存储权限";
        return "已允许";
    }

    private String batteryStatus() {
        PowerManager powerManager = (PowerManager) getSystemService(Context.POWER_SERVICE);
        if (Build.VERSION.SDK_INT >= 23 && powerManager != null) return powerManager.isIgnoringBatteryOptimizations(getPackageName()) ? "已忽略电池优化" : "建议允许忽略电池优化";
        return "系统默认";
    }

    private void requestIgnoreBatteryOptimizations() {
        try {
            PowerManager powerManager = (PowerManager) getSystemService(Context.POWER_SERVICE);
            if (Build.VERSION.SDK_INT >= 23 && powerManager != null && !powerManager.isIgnoringBatteryOptimizations(getPackageName())) {
                Intent intent = new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS);
                intent.setData(Uri.parse("package:" + getPackageName()));
                startActivity(intent);
            } else {
                startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS));
            }
        } catch (Exception e) {
            startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS));
        }
    }

    private void requestFileAccess() {
        try {
            if (Build.VERSION.SDK_INT >= 30 && !Environment.isExternalStorageManager()) {
                Intent intent = new Intent(Settings.ACTION_MANAGE_APP_ALL_FILES_ACCESS_PERMISSION);
                intent.setData(Uri.parse("package:" + getPackageName()));
                startActivity(intent);
                return;
            }
        } catch (Exception ignored) {}
        requestStoragePermissionIfNeeded();
    }

    private void requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT >= 33
            && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[] {Manifest.permission.POST_NOTIFICATIONS}, 1002);
        }
    }

    private void requestStoragePermissionIfNeeded() {
        if (Build.VERSION.SDK_INT >= 23 && Build.VERSION.SDK_INT <= 32
            && checkSelfPermission(Manifest.permission.READ_EXTERNAL_STORAGE) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[] {Manifest.permission.READ_EXTERNAL_STORAGE}, 1003);
        }
    }

    private String defaultDeviceId() {
        String model = android.os.Build.MODEL == null ? "android" : android.os.Build.MODEL.replaceAll("\\s+", "-");
        return model.length() == 0 ? "android" : model;
    }

    private boolean isNight() {
        int mode = getResources().getConfiguration().uiMode & Configuration.UI_MODE_NIGHT_MASK;
        return mode == Configuration.UI_MODE_NIGHT_YES;
    }

    private Colors colors() {
        if (isNight()) {
            return new Colors(
                Color.rgb(15, 18, 24),
                Color.rgb(30, 35, 46),
                Color.rgb(37, 44, 57),
                Color.rgb(47, 55, 70),
                Color.rgb(235, 240, 248),
                Color.rgb(160, 170, 185),
                Color.rgb(114, 126, 145),
                Color.rgb(86, 128, 255));
        }
        return new Colors(
            Color.rgb(245, 247, 251),
            Color.WHITE,
            Color.rgb(248, 250, 253),
            Color.rgb(222, 228, 238),
            Color.rgb(24, 32, 44),
            Color.rgb(91, 103, 120),
            Color.rgb(136, 148, 166),
            Color.rgb(31, 102, 255));
    }

    private static final class Colors {
        final int background;
        final int surface;
        final int input;
        final int stroke;
        final int text;
        final int muted;
        final int placeholder;
        final int accent;
        final int button;

        Colors(int background, int surface, int input, int stroke, int text, int muted, int placeholder, int accent) {
            this.background = background;
            this.surface = surface;
            this.input = input;
            this.stroke = stroke;
            this.text = text;
            this.muted = muted;
            this.placeholder = placeholder;
            this.accent = accent;
            this.button = input;
        }
    }
}
