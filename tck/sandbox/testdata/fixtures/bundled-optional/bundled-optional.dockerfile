FROM scratch
COPY review.skill /usr/share/kit-tck/bundled/review/SKILL.md
COPY reference.txt /usr/share/kit-tck/bundled/review/references/value.txt
COPY --chmod=0755 read.sh /usr/share/kit-tck/bundled/review/scripts/read.sh
COPY --chmod=0755 register.sh /usr/share/kit-tck/bundled/review/scripts/register.sh
