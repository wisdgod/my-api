import os
import re

def replace_in_file(file_path, replacements):
    with open(file_path, 'r', encoding='utf-8', newline='') as file:
        content = file.read()

    for old, new in replacements:
        content = content.replace(old, new)

    with open(file_path, 'w', encoding='utf-8', newline='\n') as file:
        file.write(content)

def recursive_replace(directory, replacements, script_name):
    for root, dirs, files in os.walk(directory):
        # 跳过以.开头的目录
        dirs[:] = [d for d in dirs if not d.startswith('.')]

        for file in files:
            # 跳过脚本自身
            if file == script_name:
                continue

            file_path = os.path.join(root, file)
            try:
                replace_in_file(file_path, replacements)
                print(f"处理文件: {file_path}")
            except Exception as e:
                print(f"处理文件 {file_path} 时出错: {str(e)}")

if __name__ == "__main__":
    # 获取当前脚本所在目录和脚本名称
    current_directory = os.path.dirname(os.path.abspath(__file__))
    script_name = os.path.basename(__file__)

    # 定义替换规则
    replacements = [
        ("Calcium-Ion/new-api", "wisdgod/my-api"),
        ("calciumion/new-api", "wisdgod/my-api"),
        ("NewAPI", "MyAPI"),
        ("New API", "My API"),
        ("New-API", "My-API"),
        ("New_API", "My_API"),
        ("newapi", "myapi"),
        ("new api", "my api"),
        ("new-api", "my-api"),
        ("new_api", "my_api")
    ]

    # 执行递归替换
    recursive_replace(current_directory, replacements, script_name)

    print("替换完成!")