# Docker 镜像构建指南

## 🚀 All-in-One 单一镜像（推荐用于生产环境）

### 架构说明

单一镜像包含：
- **Backend**: Go 应用（监听 8080 端口）
- **Frontend**: React/Vite 静态文件
- **Nginx**: 反向代理 + 静态文件服务
- **Supervisor**: 进程管理（管理 Nginx 和 Backend）

### 构建 All-in-One 镜像

```bash
# 使用 docker-compose（推荐）
docker-compose -f docker-compose.all-in-one.yml build

# 构建并启动
docker-compose -f docker-compose.all-in-one.yml up -d --build

# 使用 docker build 直接构建
docker build -f docker/Dockerfile.all-in-one -t nofx-all-in-one:latest .
```

### 运行 All-in-One 容器

```bash
# 使用 docker-compose
docker-compose -f docker-compose.all-in-one.yml up -d

# 或直接运行
docker run -d \
  --name nofx-all-in-one \
  -p 80:80 \
  -v $(pwd)/backend/config.toml:/app/config.toml:ro \
  -v $(pwd)/backend/data:/app/data \
  -v $(pwd)/decision_logs:/app/decision_logs \
  nofx-all-in-one:latest
```

### 查看日志

```bash
# 查看所有进程日志（Supervisor）
docker logs -f nofx-all-in-one

# 进入容器查看 Supervisor 状态
docker exec -it nofx-all-in-one supervisorctl status
```

### 优势

- ✅ 单一镜像，部署简单
- ✅ 进程自动管理（Supervisor）
- ✅ 资源占用更少（共享基础镜像）
- ✅ 适合单机部署和容器编排

---

## 方法一：使用 docker-compose 构建（分离式部署）

### 构建所有服务镜像

```bash
# 构建所有服务（backend + frontend）
docker-compose build

# 构建并启动服务
docker-compose up -d --build

# 只构建特定服务
docker-compose build nofx          # 只构建 backend
docker-compose build nofx-frontend # 只构建 frontend
```

### 查看构建的镜像

```bash
docker images | grep nofx
```

---

## 方法二：使用 docker build 单独构建

### 构建 Backend 镜像

```bash
# 从项目根目录执行
docker build -f docker/Dockerfile.backend -t nofx-backend:latest .

# 或者指定版本标签
docker build -f docker/Dockerfile.backend -t nofx-backend:v1.0.0 .
```

### 构建 Frontend 镜像

```bash
# 从项目根目录执行
docker build -f docker/Dockerfile.frontend -t nofx-frontend:latest .

# 或者指定版本标签
docker build -f docker/Dockerfile.frontend -t nofx-frontend:v1.0.0 .
```

---

## 方法三：构建并打标签（用于推送到镜像仓库）

### 构建并打标签

```bash
# Backend 镜像
docker build -f docker/Dockerfile.backend -t nofx-backend:latest .
docker tag nofx-backend:latest your-registry/nofx-backend:latest
docker tag nofx-backend:latest your-registry/nofx-backend:v1.0.0

# Frontend 镜像
docker build -f docker/Dockerfile.frontend -t nofx-frontend:latest .
docker tag nofx-frontend:latest your-registry/nofx-frontend:latest
docker tag nofx-frontend:latest your-registry/nofx-frontend:v1.0.0
```

### 推送到镜像仓库

```bash
# 登录到镜像仓库（以 Docker Hub 为例）
docker login

# 推送镜像
docker push your-registry/nofx-backend:latest
docker push your-registry/nofx-backend:v1.0.0
docker push your-registry/nofx-frontend:latest
docker push your-registry/nofx-frontend:v1.0.0
```

**常用镜像仓库示例：**
- Docker Hub: `username/nofx-backend:latest`
- 阿里云: `registry.cn-hangzhou.aliyuncs.com/namespace/nofx-backend:latest`
- 腾讯云: `ccr.ccs.tencentyun.com/namespace/nofx-backend:latest`

---

## 方法四：使用 Makefile 快速构建（可选）

可以添加以下内容到 Makefile：

```makefile
.PHONY: docker-build
docker-build:
	@echo "Building Docker images..."
	docker-compose build

.PHONY: docker-build-backend
docker-build-backend:
	@echo "Building backend image..."
	docker build -f docker/Dockerfile.backend -t nofx-backend:latest .

.PHONY: docker-build-frontend
docker-build-frontend:
	@echo "Building frontend image..."
	docker build -f docker/Dockerfile.frontend -t nofx-frontend:latest .

.PHONY: docker-push
docker-push:
	@echo "Pushing images to registry..."
	docker push your-registry/nofx-backend:latest
	docker push your-registry/nofx-frontend:latest
```

然后使用：
```bash
make docker-build          # 构建所有镜像
make docker-build-backend  # 只构建 backend
make docker-build-frontend # 只构建 frontend
```

---

## 验证镜像

### 查看镜像信息

```bash
# 查看所有 nofx 相关镜像
docker images | grep nofx

# 查看镜像详细信息
docker inspect nofx-backend:latest
docker inspect nofx-frontend:latest
```

### 测试运行镜像

```bash
# 测试 backend 镜像
docker run --rm -p 8080:8080 \
  -v $(pwd)/backend/config.toml:/app/config.toml:ro \
  nofx-backend:latest

# 测试 frontend 镜像
docker run --rm -p 3000:80 nofx-frontend:latest
```

---

## 优化构建（使用 BuildKit）

### 启用 BuildKit 加速构建

```bash
# 设置环境变量
export DOCKER_BUILDKIT=1
export COMPOSE_DOCKER_CLI_BUILD=1

# 然后正常构建
docker-compose build
```

### 使用缓存优化

```bash
# 构建时使用缓存
docker-compose build --parallel

# 不使用缓存（完全重新构建）
docker-compose build --no-cache
```

---

## 常见问题

### 1. 构建时内存不足

如果遇到内存不足，可以：
- 增加 Docker 的内存限制
- 使用 `--memory` 参数限制构建时的内存使用

### 2. 构建速度慢

- 使用 BuildKit: `DOCKER_BUILDKIT=1 docker-compose build`
- 使用多阶段构建缓存（已配置）
- 使用国内镜像源加速

### 3. 镜像体积过大

当前配置已使用多阶段构建优化，镜像体积应该已经最小化。如需进一步优化：
- 使用 `.dockerignore` 排除不必要的文件
- 使用 Alpine 基础镜像（已使用）

---

## 快速开始

### All-in-One 方式（推荐）

```bash
# 1. 构建单一镜像
docker-compose -f docker-compose.all-in-one.yml build

# 2. 启动服务
docker-compose -f docker-compose.all-in-one.yml up -d

# 3. 查看日志
docker-compose -f docker-compose.all-in-one.yml logs -f

# 4. 停止服务
docker-compose -f docker-compose.all-in-one.yml down
```

### 分离式部署方式

```bash
# 1. 构建所有镜像
docker-compose build

# 2. 启动服务
docker-compose up -d

# 3. 查看日志
docker-compose logs -f

# 4. 停止服务
docker-compose down
```

---

## 架构对比

### All-in-One 架构
```
┌─────────────────────────────────┐
│   Docker Container              │
│  ┌──────────┐  ┌──────────┐   │
│  │ Backend  │  │  Nginx   │   │
│  │ :8080    │  │  :80     │   │
│  └──────────┘  └──────────┘   │
│       ↑              ↑          │
│       └──────┬───────┘          │
│              │                  │
│       ┌──────▼──────┐           │
│       │ Supervisor  │           │
│       └─────────────┘           │
└─────────────────────────────────┘
```

### 分离式架构
```
┌──────────────┐    ┌──────────────┐
│ Backend      │    │ Frontend    │
│ Container    │    │ Container    │
│ :8080        │    │ :80 (Nginx) │
└──────────────┘    └──────────────┘
```

**选择建议：**
- **All-in-One**: 适合单机部署、资源受限环境、简单运维
- **分离式**: 适合微服务架构、需要独立扩缩容、多实例部署

