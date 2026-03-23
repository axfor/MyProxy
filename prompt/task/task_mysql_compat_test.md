任务：可以通过mysql、tidb等语法等测试集合验证我们aproxy的兼容性，正确性

参考资料：
1、git clone -b 5.7 https://github.com/mysql/mysql-server.git 的  mysql-5.7/mysql-test/suite
2、https://github.com/pingcap/tidb

要求：
1、sql语法全面，都通过，不了规避实现
2、深入分析一下tidb对mysql的语法语义解析