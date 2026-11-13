package com.phj2ll.readalong;

import android.os.Bundle;
import android.webkit.WebView;

import androidx.activity.OnBackPressedCallback;

import com.getcapacitor.BridgeActivity;

public class MainActivity extends BridgeActivity {

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        // 系统返回（Android 的手势 / 返回键）先交给 WebView 的历史：
        // 书架 → 本书的课 → 课程，每翻一页都会进一条历史，能退就退一页；
        // 已经在书架首页才真的退出 App。
        //
        // Capacitor 7 的 BridgeActivity 自己不管返回键，而 targetSdk 35 在
        // Android 15 上默认启用预测性返回（Predictive Back），不接管的话
        // 手指一滑系统直接把 Activity 关掉，看起来就是「一退就退出」。
        // 注册在 OnBackPressedDispatcher 上的回调会被 androidx 自动桥接到
        // OnBackInvokedDispatcher，新旧系统都能收到。
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                WebView web = bridge != null ? bridge.getWebView() : null;
                if (web != null && web.canGoBack()) {
                    web.goBack();
                    return;
                }
                // 没有上一页了：结束 Activity（回到桌面）
                finish();
            }
        });
    }
}
