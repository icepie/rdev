#!/bin/bash
# Docker构建脚本 - 提高二进制兼容性

set -e

echo "=== Weylus Docker构建脚本 ==="
echo "使用Ubuntu 20.04基础镜像 (GLIBC 2.31)"
echo ""

# 创建输出目录
mkdir -p output

# 构建Docker镜像
echo "步骤1: 构建Docker镜像..."
docker build -f Dockerfile.build -t weylus-builder .

# 运行容器并编译
echo ""
echo "步骤2: 编译Weylus..."
docker run --rm -v "$(pwd)/output:/output" weylus-builder

# 检查编译结果
if [ -f "output/weylus" ]; then
    echo ""
    echo "✅ 编译成功！"
    echo "二进制文件位置: $(pwd)/output/weylus"
    echo ""
    echo "检查GLIBC依赖:"
    ldd output/weylus | grep GLIBC || true
    echo ""
    echo "文件信息:"
    file output/weylus
    echo ""
    echo "现在可以运行: ./output/weylus"
else
    echo ""
    echo "❌ 编译失败！"
    exit 1
fi
