#!/bin/bash
# 使用代理的Docker构建脚本

set -e

# 配置代理
PROXY="http://192.168.2.222:12333"

echo "=== Weylus Docker构建 (使用代理) ==="
echo "代理: $PROXY"
echo ""

# 创建输出目录
mkdir -p output

# 构建Docker镜像（使用代理）
echo "步骤1: 构建Docker镜像..."
docker build -f Dockerfile.build \
  --build-arg http_proxy=$PROXY \
  --build-arg https_proxy=$PROXY \
  -t weylus-builder .

# 运行容器并编译（容器内也使用代理）
echo ""
echo "步骤2: 编译Weylus..."
docker run --rm \
  -e http_proxy=$PROXY \
  -e https_proxy=$PROXY \
  -v "$(pwd)/output:/output" \
  weylus-builder

# 检查编译结果
if [ -f "output/weylus" ]; then
    echo ""
    echo "✅ 编译成功！"
    echo "二进制文件: $(pwd)/output/weylus"
    echo ""
    chmod +x output/weylus
    echo "现在可以运行: ./output/weylus"
else
    echo ""
    echo "❌ 编译失败！"
    exit 1
fi
