package cn.singzer.rdev.android;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.media.projection.MediaProjectionManager;
import android.net.Uri;
import android.os.Bundle;
import android.provider.Settings;
import android.view.Gravity;
import android.view.View;
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

    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        prefs = getSharedPreferences("rdev", MODE_PRIVATE);
        setContentView(createContentView());
    }

    private View createContentView() {
        ScrollView scroll = new ScrollView(this);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(32, 32, 32, 32);
        scroll.addView(root);

        TextView title = new TextView(this);
        title.setText("RDev Android 被控端");
        title.setTextSize(22);
        title.setGravity(Gravity.CENTER_HORIZONTAL);
        root.addView(title, matchWrap());

        serverField = input("Server", prefs.getString("server", "wss://rdev.singzer.cn"));
        idField = input("Device ID", prefs.getString("id", defaultDeviceId()));
        passwordField = input("Password", prefs.getString("password", ""));
        root.addView(serverField, matchWrap());
        root.addView(idField, matchWrap());
        root.addView(passwordField, matchWrap());

        Button save = button("保存配置");
        save.setOnClickListener(v -> saveConfig());
        root.addView(save, matchWrap());

        Button capture = button("授权录屏并启动高性能编码测试");
        capture.setOnClickListener(v -> requestProjection());
        root.addView(capture, matchWrap());

        Button stop = button("停止服务");
        stop.setOnClickListener(v -> stopService(new Intent(this, RDevAgentService.class)));
        root.addView(stop, matchWrap());

        Button accessibility = button("打开无障碍输入权限");
        accessibility.setOnClickListener(v -> startActivity(new Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS)));
        root.addView(accessibility, matchWrap());

        Button appSettings = button("打开应用设置");
        appSettings.setOnClickListener(v -> startActivity(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:" + getPackageName()))));
        root.addView(appSettings, matchWrap());

        statusView = new TextView(this);
        statusView.setText("本地 MVP：先验证 MediaProjection + MediaCodec H.264 硬编码。\n终端/Termux 不在 APK 范围内。");
        statusView.setPadding(0, 24, 0, 0);
        root.addView(statusView, matchWrap());
        return scroll;
    }

    private EditText input(String hint, String value) {
        EditText field = new EditText(this);
        field.setHint(hint);
        field.setSingleLine(true);
        field.setText(value);
        return field;
    }

    private Button button(String text) {
        Button button = new Button(this);
        button.setText(text);
        return button;
    }

    private LinearLayout.LayoutParams matchWrap() {
        return new LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
    }

    private void saveConfig() {
        prefs.edit()
            .putString("server", serverField.getText().toString().trim())
            .putString("id", idField.getText().toString().trim())
            .putString("password", passwordField.getText().toString())
            .apply();
        statusView.setText("配置已保存");
    }

    private void requestProjection() {
        saveConfig();
        MediaProjectionManager manager = (MediaProjectionManager) getSystemService(Context.MEDIA_PROJECTION_SERVICE);
        startActivityForResult(manager.createScreenCaptureIntent(), REQUEST_MEDIA_PROJECTION);
    }

    @Override protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode != REQUEST_MEDIA_PROJECTION) return;
        if (resultCode != RESULT_OK || data == null) {
            statusView.setText("录屏授权已取消");
            return;
        }
        Intent intent = new Intent(this, RDevAgentService.class);
        intent.setAction(RDevAgentService.ACTION_START_CAPTURE);
        intent.putExtra(RDevAgentService.EXTRA_RESULT_CODE, resultCode);
        intent.putExtra(RDevAgentService.EXTRA_RESULT_DATA, data);
        startService(intent);
        statusView.setText("服务已启动，请查看 adb logcat -s RDevAgent RDevCapture RDevEncoder");
    }

    private String defaultDeviceId() {
        String model = android.os.Build.MODEL == null ? "android" : android.os.Build.MODEL.replaceAll("\\s+", "-");
        return model.length() == 0 ? "android" : model;
    }
}
