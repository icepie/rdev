cmake_minimum_required(VERSION 3.20)
project(rdev_client_cpp LANGUAGES C CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
set(CMAKE_AUTOMOC ON)
set(BUILD_SHARED_LIBS OFF)

find_package(Qt6 REQUIRED COMPONENTS Core Network Widgets)
find_package(OpenSSL REQUIRED)
find_package(PkgConfig REQUIRED)
pkg_check_modules(FFMPEG REQUIRED IMPORTED_TARGET libavcodec libavutil libswscale)
pkg_check_modules(SDL2 REQUIRED IMPORTED_TARGET sdl2 opus)

set(MOONLIGHT_BACKEND "${CMAKE_CURRENT_SOURCE_DIR}/../../third_party/moonlight-web/backend")
add_subdirectory("${MOONLIGHT_BACKEND}/third_party/moonlight-common-c"
                 "${CMAKE_CURRENT_BINARY_DIR}/moonlight-common-c")

add_executable(rdev-client-cpp
    src/main.cpp
    src/gamestream_session.h
    src/gamestream_session.cpp
    src/viewer_window.h
    src/viewer_window.cpp
    "${MOONLIGHT_BACKEND}/src/backend/HostCertCheck.cpp"
    "${MOONLIGHT_BACKEND}/src/backend/IdentityManager.cpp"
    "${MOONLIGHT_BACKEND}/src/backend/NvHTTP.cpp"
    "${MOONLIGHT_BACKEND}/src/backend/NvPairingManager.cpp"
    "${MOONLIGHT_BACKEND}/src/common/Logger.cpp"
)
target_include_directories(rdev-client-cpp PRIVATE
    "${MOONLIGHT_BACKEND}/src"
    "${MOONLIGHT_BACKEND}/third_party/moonlight-common-c/src"
)
target_link_libraries(rdev-client-cpp PRIVATE
    Qt6::Core Qt6::Network Qt6::Widgets
    moonlight-common-c OpenSSL::Crypto
    PkgConfig::FFMPEG PkgConfig::SDL2
)
target_compile_options(rdev-client-cpp PRIVATE -Wall -Wextra -Wpedantic)
