#!/bin/bash
# 改进的Docker构建脚本 - 支持多种选项

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 默认配置
DOCKERFILE="Dockerfile.build"
BASE_IMAGE="ubuntu:20.04"
USE_CACHE=true
OUTPUT_DIR="output"

# 帮助信息
show_help() {
    cat << EOF
Weylus Docker构建脚本

用法: $0 [选项]

选项:
    -h, --help              显示此帮助信息
    -f, --dockerfile FILE   使用指定的Dockerfile (默认: Dockerfile.build)
    -b, --base IMAGE        使用指定的基础镜像 (默认: ubuntu:20.04)
    -o, --output DIR        输出目录 (默认: output)
    --no-cache              不使用Docker缓存
    --multistage            使用多阶段构建Dockerfile
    --ubuntu18              使用Ubuntu 18.04 (更高兼容性)
    --ubuntu22              使用Ubuntu 22.04 (较新)

示例:
    $0                      # 默认构建 (Ubuntu 20.04)
    $0 --ubuntu18           # 使用Ubuntu 18.04提高兼容性
    $0 --multistage         # 使用多阶段构建
    $0 --no-cache           # 强制重新构建

EOF
    exit 0
}

# 解析参数
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            show_help
            ;;
        -f|--dockerfile)
            DOCKERFILE="$2"
            shift 2
            ;;
        -b|--base)
            BASE_IMAGE="$2"
            shift 2
            ;;
        -o|--output)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        --no-cache)
            USE_CACHE=false
            shift
            ;;
        --multistage)
            DOCKERFILE="Dockerfile.multistage"
            shift
            ;;
        --ubuntu18)
            BASE_IMAGE="ubuntu:18.04"
            shift
            ;;
        --ubuntu22)
            BASE_IMAGE="ubuntu:22.04"
            shift
            ;;
        *)
            echo -e "${RED}未知选项: $1${NC}"
            echo "使用 -h 或 --help 查看帮助"
            exit 1
            ;;
    esac
done

# 打印配置
echo -e "${BLUE}=== Weylus Docker构建 ===${NC}"
echo -e "Dockerfile: ${YELLOW}${DOCKERFILE}${NC}"
echo -e "基础镜像: ${YELLOW}${BASE_IMAGE}${NC}"
echo -e "输出目录: ${YELLOW}${OUTPUT_DIR}${NC}"
echo -e "使用缓存: ${YELLOW}${USE_CACHE}${NC}"
echo ""

# 检查Docker是否安装
if ! command -v docker &> /dev/null; then
    echo -e "${RED}错误: 未找到Docker，请先安装Docker${NC}"
    exit 1
fi

# 检查Dockerfile是否存在
if [ ! -f "$DOCKERFILE" ]; then
    echo -e "${RED}错误: Dockerfile不存在: $DOCKERFILE${NC}"
    exit 1
fi

# 创建输出目录
mkdir -p "$OUTPUT_DIR"

# 构建Docker镜像
echo -e "${BLUE}步骤1: 构建Docker镜像...${NC}"

BUILD_ARGS="--build-arg BASE_IMAGE=${BASE_IMAGE}"
if [ "$USE_CACHE" = false ]; then
    BUILD_ARGS="$BUILD_ARGS --no-cache"
fi

if docker build -f "$DOCKERFILE" $BUILD_ARGS -t weylus-builder .; then
    echo -e "${GREEN}✅ 镜像构建成功${NC}"
else
    echo -e "${RED}❌ 镜像构建失败${NC}"
    exit 1
fi

# 运行容器并编译
echo ""
echo -e "${BLUE}步骤2: 编译Weylus...${NC}"

if docker run --rm -v "$(pwd)/$OUTPUT_DIR:/output" weylus-builder; then
    echo -e "${GREEN}✅ 编译成功${NC}"
else
    echo -e "${RED}❌ 编译失败${NC}"
    exit 1
fi

# 检查编译结果
if [ ! -f "$OUTPUT_DIR/weylus" ]; then
    echo -e "${RED}❌ 未找到编译产物${NC}"
    exit 1
fi

# 显示结果信息
echo ""
echo -e "${GREEN}=== 构建完成 ===${NC}"
echo -e "二进制位置: ${YELLOW}$(pwd)/$OUTPUT_DIR/weylus${NC}"
echo ""

# 显示文件信息
echo -e "${BLUE}文件信息:${NC}"
file "$OUTPUT_DIR/weylus"
echo ""
echo -e "${BLUE}文件大小:${NC}"
du -h "$OUTPUT_DIR/weylus"
echo ""

# 检查GLIBC依赖
echo -e "${BLUE}GLIBC依赖:${NC}"
if command -v ldd &> /dev/null; then
    ldd "$OUTPUT_DIR/weylus" | grep -E "GLIBC|not found" || echo "无法确定依赖"
else
    echo "ldd命令不可用，跳过依赖检查"
fi
echo ""

# 添加执行权限
chmod +x "$OUTPUT_DIR/weylus"
echo -e "${GREEN}✅ 已添加执行权限${NC}"
echo ""

# 使用提示
echo -e "${BLUE}使用方法:${NC}"
echo "  ./$OUTPUT_DIR/weylus --help    # 查看帮助"
echo "  ./$OUTPUT_DIR/weylus           # 启动服务器"
echo ""
echo -e "${YELLOW}注意: 请确保在X11环境中运行，并设置了DISPLAY环境变量${NC}"
