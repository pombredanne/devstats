select
  a.login,
  count(*) as reviewers_count
from
  gha_events e,
  gha_actors a
where e.id in
  (
    select
      min(event_id)
    from
      gha_issues_labels
    where
      label_id in (
        select id from gha_labels where name in ('lgtm', 'LGTM')
      )
    group by issue_id
    union
    select
      event_id
    from
      gha_texts
    where
      preg_rlike('{^\\s*lgtm\\s*$}i', body)
  )
and e.actor_id = a.id
and a.login not in ('googlebot')
and a.login not like 'k8s-%'
group by a.login
order by
  reviewers_count desc,
  a.login asc
