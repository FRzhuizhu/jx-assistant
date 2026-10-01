@echo off
title 语音诊断（独立窗口，用完关掉即可）
rem 用「带探针的前端 + 修好麦克风权限的桌面壳」开一个独立窗口：端口 8897、
rem 数据目录独立，不影响你正在用的正式程序。窗口里会自动把环境信息与每次
rem 点麦克风的结果写进 probe-wv\data\desktop.log，供数字助理读取分析。
rem 注意：桌面壳是单实例的，跑这个之前要先关掉正式窗口。
cd /d "%~dp0.jxai\tmp\voicetest\probe-wv"
start "" "%~dp0坚祥数字助理GO-语音修复.exe" -port 8897 -webdir "%~dp0.jxai\tmp\voicetest\web-probe-wv"
