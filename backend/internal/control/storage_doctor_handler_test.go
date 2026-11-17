package control

import "testing"

func TestParseDovecotHome(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "tab separated",
			output: "field\tvalue\nuid\t5000\ngid\t5000\nhome\t/srv/a8m-storage/mail/lab.test/user\nmail\tmaildir:~/Maildir\n",
			want:   "/srv/a8m-storage/mail/lab.test/user",
		},
		{
			name:   "colon separated",
			output: "uid: 5000\ngid: 5000\nhome: /var/vmail/lab.test/user\n",
			want:   "/var/vmail/lab.test/user",
		},
		{
			name:   "no home",
			output: "field\tvalue\nuid\t5000\n",
			want:   "",
		},
		{
			name:   "empty",
			output: "",
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDovecotHome(tc.output); got != tc.want {
				t.Fatalf("parseDovecotHome() = %q, want %q", got, tc.want)
			}
		})
	}
}
