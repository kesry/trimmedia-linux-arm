# 打包 apps/ 下的所有项目，最终产物汇总到 out/
#
# 用法:
#   make            # 打包全部项目
#   make list       # 列出所有可单独打包的项目
#   make clean      # 清理 out/ 及各项目临时产物

# 项目根目录
ROOT := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))

# 输出目录
OUT_DIR := $(ROOT)out

# 自动发现 apps/ 下包含 package.sh 的项目目录
APPS := $(notdir $(patsubst %/,%,$(sort $(dir $(wildcard $(ROOT)apps/*/package.sh)))))

# 每个项目打包后的 tgz 产物
PRODUCTS := $(addprefix $(OUT_DIR)/,$(addsuffix .tgz,$(APPS)))

.PHONY: all clean list $(APPS)

all: $(PRODUCTS)
	@echo "=============================================="
	@echo "所有项目打包完成，产物位于 $(OUT_DIR)/:"
	@ls -lh $(PRODUCTS)
	@echo "=============================================="

# 将各项目 package.sh 生成的 tgz 汇总到 out/，打包完一个就清掉该项目的 output 目录
$(OUT_DIR)/%.tgz: $(ROOT)apps/%/package.sh
	@mkdir -p $(OUT_DIR)
	@echo ">>> 打包项目: $*"
	bash $<
	@cp -f $(ROOT)apps/$*/output/$*.tgz $@
	@rm -rf $(ROOT)apps/$*/output
	@echo "<<< $* -> $@ (已清理 apps/$*/output)"

# 单独打包某个项目，例如: make fntv
$(APPS): %: $(OUT_DIR)/%.tgz
	@

# 列出所有可单独打包的项目
list:
	@echo "可单独打包的项目（make <项目名>）:"
	@for app in $(APPS); do echo "  $$app"; done

clean:
	rm -rf $(OUT_DIR)
	@for app in $(APPS); do rm -rf $(ROOT)apps/$$app/bin $(ROOT)apps/$$app/output; done
	@echo "clean done"
