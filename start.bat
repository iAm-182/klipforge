@echo off
chcp 65001 >nul
cd /d "%~dp0"
title KlipForge — Go Auto-Clipper & MotionKlip Studio

if not exist "klipforge.exe" (
    echo [KlipForge] Building Go binary...
    go build -o klipforge.exe .
    if errorlevel 1 (
        echo [ERROR] Gagal build klipforge.exe! Pastikan Go terinstall.
        pause
        exit /b 1
    )
)

echo [KlipForge] Menjalankan dashboard di http://127.0.0.1:8787 ...
klipforge.exe
pause
