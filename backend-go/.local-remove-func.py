import io, re, sys

def remove_func(s, func_name):
    m = re.search(r'func ' + func_name + r'\(', s)
    if not m:
        print('MISS', func_name)
        return s
    body_start = m.start()
    end = s.find('\n}\n', body_start)
    if end < 0:
        end = len(s) - 1
    else:
        end += 3
    # swallow preceding comment lines
    head = s[:body_start]
    lines = head.rstrip('\n').split('\n')
    i = len(lines)
    while i > 0 and lines[i-1].lstrip().startswith('//'):
        i -= 1
    start = len('\n'.join(lines[:i]))
    if start > 0:
        start += 1
    return (s[:start] + s[end:]).replace('\n\n\n\n', '\n\n\n')

if __name__ == '__main__':
    path = sys.argv[1]
    s = io.open(path, encoding='utf-8').read()
    for name in sys.argv[2:]:
        s = remove_func(s, name)
    io.open(path, 'w', encoding='utf-8', newline='').write(s)
    print('ok', path)
